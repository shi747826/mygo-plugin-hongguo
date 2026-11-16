package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// LoginStatus 是扫码的进度。
type LoginStatus string

const (
	// LoginPending 表示等待用户在手机上确认。
	LoginPending LoginStatus = "pending"
	// LoginConfirmed 表示已确认并取得登录态。
	LoginConfirmed LoginStatus = "confirmed"
	// LoginExpired 表示二维码已过期，需要重新获取。
	LoginExpired LoginStatus = "expired"
)

// Challenge 是一次扫码登录：二维码图片、登录票据与有效期。
// Token 只用于轮询，不写入磁盘、不写日志。
type Challenge struct {
	Token   string
	Image   []byte
	Expires time.Time
}

// envelope 是 passport 接口的统一外层。
type envelope struct {
	Message   string `json:"message"`
	ErrorCode int    `json:"error_code"`
	Code      int    `json:"code"`
	Data      struct {
		Token       string          `json:"token"`
		QRCode      string          `json:"qrcode"`
		ExpireTime  int64           `json:"expire_time"`
		Status      json.RawMessage `json:"status"`
		RedirectURL string          `json:"redirect_url"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
	} `json:"data"`
}

func (e envelope) failed() error {
	msg := strings.TrimSpace(e.Data.Description)
	if msg == "" {
		msg = strings.TrimSpace(e.Message)
	}
	if e.ErrorCode == 0 && e.Code == 0 && e.Data.ErrorCode == 0 && (msg == "" || strings.EqualFold(msg, "success")) {
		return nil
	}
	// Server descriptions can contain credentials; expose only numeric codes.
	return fmt.Errorf("%w: 账号响应错误 (%d/%d/%d)", ErrUpstream, e.Code, e.ErrorCode, e.Data.ErrorCode)
}

// StartLogin 获取一张登录二维码。返回的图片是服务端直接给出的 PNG。
func (c *Client) StartLogin(ctx context.Context) (Challenge, error) {
	body, err := c.getJSON(ctx, "/passport/web/get_qrcode/", baseQuery())
	if err != nil {
		return Challenge{}, err
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Challenge{}, fmt.Errorf("%w: 二维码响应无法解析", ErrUpstream)
	}
	if err := env.failed(); err != nil {
		return Challenge{}, err
	}
	token := strings.TrimSpace(env.Data.Token)
	source := strings.TrimSpace(env.Data.QRCode)
	if token == "" || source == "" {
		return Challenge{}, fmt.Errorf("%w: 二维码格式已变化", ErrUpstream)
	}
	raw := source
	if i := strings.Index(raw, "base64,"); i >= 0 {
		raw = raw[i+len("base64,"):]
	}
	image, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(image) == 0 {
		return Challenge{}, fmt.Errorf("%w: 二维码图片无法解码", ErrUpstream)
	}
	expires := time.Now().Add(3 * time.Minute)
	if env.Data.ExpireTime > time.Now().Unix() {
		expires = time.Unix(env.Data.ExpireTime, 0)
	}
	return Challenge{Token: token, Image: image, Expires: expires}, nil
}

// PollLogin 查询扫码结果。确认时登录 Cookie 已进入客户端自己的 CookieJar。
func (c *Client) PollLogin(ctx context.Context, token string) (LoginStatus, error) {
	if strings.TrimSpace(token) == "" {
		return LoginExpired, nil
	}
	q := baseQuery()
	q.Set("token", token)
	body, err := c.getJSON(ctx, "/passport/web/check_qrconnect/", q)
	if err != nil {
		return LoginPending, err
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return LoginPending, fmt.Errorf("%w: 扫码状态无法解析", ErrUpstream)
	}
	status := strings.TrimSpace(string(env.Data.Status))
	if strings.HasPrefix(status, `"`) {
		if err := json.Unmarshal(env.Data.Status, &status); err != nil {
			return LoginPending, fmt.Errorf("%w: 扫码状态无效", ErrUpstream)
		}
	}
	status = strings.ToLower(strings.TrimSpace(status))
	// 过期是终态：即使外层同时带了错误文案，也应让调用方提示重新扫码。
	// 实测线上未扫码时返回 "new"；5/6/expired 为过期。
	if status == "5" || status == "6" || status == "expired" || status == "timeout" {
		return LoginExpired, nil
	}
	if err := env.failed(); err != nil {
		return LoginPending, err
	}
	switch status {
	case "new", "1", "2", "pending", "scanned", "scan", "waiting":
		return LoginPending, nil
	case "3", "confirmed":
		// 确认后必须先建立会话，再用资料接口验证；跳转只允许官方 HTTPS。
		redirect := strings.TrimSpace(env.Data.RedirectURL)
		followed := false
		if c.CookieHeader() == "" && redirect != "" {
			if err := c.followOfficial(ctx, redirect); err != nil {
				return LoginPending, err
			}
			followed = true
		}
		if _, err := c.Profile(ctx); err != nil {
			if followed || redirect == "" {
				return LoginPending, err
			}
			// 本地 Cookie 可能已失效，按服务端要求重新走一次确认跳转。
			if err := c.followOfficial(ctx, redirect); err != nil {
				return LoginPending, err
			}
			if _, err := c.Profile(ctx); err != nil {
				return LoginPending, err
			}
		}
		return LoginConfirmed, nil
	default:
		// 未知状态按「还在等待」处理：服务端随时可能新增状态值，
		// 把轮询打断会直接让扫码流程失败。
		return LoginPending, nil
	}
}

// followOfficial 跟随一次服务端确认跳转，只允许 HTTPS 官方域。
func (c *Client) followOfficial(ctx context.Context, raw string) error {
	target, err := resolveOfficial(raw)
	if err != nil {
		return err
	}
	_, err = c.getURL(ctx, target)
	return err
}

// CookieHeader 返回当前登录态的 Cookie 串，仅用于本地持久化。
func (c *Client) CookieHeader() string {
	u, err := url.Parse(webBase)
	if err != nil {
		return ""
	}
	var parts []string
	for _, ck := range c.http.Jar.Cookies(u) {
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	return strings.Join(parts, "; ")
}

// SetCookieHeader 恢复保存过的登录态。
func (c *Client) SetCookieHeader(header string) {
	u, err := url.Parse(webBase)
	if err != nil {
		return
	}
	var cookies []*http.Cookie
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		ck := &http.Cookie{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value), Path: "/", Secure: true, HttpOnly: true}
		if ck.Valid() == nil {
			cookies = append(cookies, ck)
		}
	}
	if len(cookies) > 0 {
		c.http.Jar.SetCookies(u, cookies)
	}
}

// ClearCookies 丢弃当前登录态。
func (c *Client) ClearCookies() {
	jar, _ := cookiejar.New(nil)
	c.http.Jar = jar
}
