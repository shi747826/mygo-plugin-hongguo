// Package account 处理红果网页端的扫码登录与账号资料。
//
// 它不复用 openhongguo 的匿名设备体系：SDK 会主动拒绝携带凭据的请求，
// 而网页登录态是另一套 Cookie 会话。网页登录访问 hongguoduanju.com；
// 用户授权的个人数据读取使用官方 App API 域。凭据不发送给第三方站点，
// 也不记录 URL、token 或 Cookie。
package account

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	webBase   = "https://hongguoduanju.com"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
	// 网页端 passport 的固定参数，实测缺少它们会返回「缺少参数」。
	aid = "513755"
	// maxBody 限制响应体读取，避免异常响应撑爆内存。
	maxBody = 2 << 20
)

// ErrSessionExpired 表示本地保存的会话已失效，需要重新扫码。
var ErrSessionExpired = errors.New("登录已过期，请重新扫码")

// ErrUpstream 表示红果接口返回了错误或无法解析的内容。
var ErrUpstream = errors.New("红果账号接口暂时不可用")

// Client 是网页账号接口的客户端，所有请求都受 context 约束。
type Client struct {
	http *http.Client
	// appBase 是 App 域接口的主机，测试时指向 httptest 服务器。
	appBase string
}

// New 创建一个账号客户端；QRCookieJar 保存二维码会话期间的 Cookie。
func New() *Client {
	jar, _ := cookiejar.New(nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Client{appBase: appHost, http: &http.Client{
		Timeout:       15 * time.Second,
		Jar:           jar,
		Transport:     transport,
		CheckRedirect: safeRedirect,
	}}
}

// safeRedirect 只允许跳转到官方 HTTPS 域，且不把凭据带过跳转边界。
func safeRedirect(req *http.Request, via []*http.Request) error {
	if !sameHost(req.URL) {
		return fmt.Errorf("拒绝跳转到非官方地址: %w", ErrUpstream)
	}
	if len(via) >= 5 {
		return fmt.Errorf("跳转次数过多: %w", ErrUpstream)
	}
	// 目标域的 Cookie 由 Jar 按域重新选择；显式清除继承来的凭据。
	req.Header.Del("Cookie")
	req.Header.Del("Authorization")
	req.Header.Del("Proxy-Authorization")
	return nil
}

func sameHost(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "hongguoduanju.com" || strings.HasSuffix(host, ".hongguoduanju.com")
}

// resolveOfficial 把服务端返回的跳转地址解析为官方 HTTPS 绝对地址；
// 相对路径按网页根地址解析，任何其他形态都拒绝。
func resolveOfficial(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: 跳转地址无效", ErrUpstream)
	}
	if !parsed.IsAbs() {
		base, _ := url.Parse(webBase + "/")
		parsed = base.ResolveReference(parsed)
	}
	if !sameHost(parsed) {
		return "", fmt.Errorf("%w: 非安全官方地址", ErrUpstream)
	}
	return parsed.String(), nil
}

// getJSON 发起一次 GET 请求并返回原始响应体。路径已固定，不拼接用户输入。
func (c *Client) getJSON(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if !strings.HasPrefix(path, "/passport/") {
		return nil, fmt.Errorf("不支持的账号接口路径: %w", ErrUpstream)
	}
	target := webBase + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return c.getURL(ctx, target)
}

func (c *Client) getURL(ctx context.Context, target string) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil || !sameHost(u) {
		return nil, fmt.Errorf("%w: 非安全官方地址", ErrUpstream)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, redact(err))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", webBase+"/")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUpstream, redact(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w（HTTP %d）", ErrUpstream, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUpstream, redact(err))
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%w: 响应过大", ErrUpstream)
	}
	return body, nil
}

// baseQuery 是网页 passport 必需的公共参数。
func baseQuery() url.Values {
	q := url.Values{}
	q.Set("aid", aid)
	q.Set("passport_jssdk_version", "3.4.10")
	q.Set("passport_jssdk_type", "normal")
	q.Set("account_sdk_source", "web")
	q.Set("is_from_ttaccountsdk", "1")
	q.Set("language", "zh")
	q.Set("next", webBase+"/")
	return q
}
