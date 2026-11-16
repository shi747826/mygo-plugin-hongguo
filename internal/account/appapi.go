package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/beilunyang/openhongguo/sdk"
	"github.com/shi747826/mygo-plugin-hongguo/internal/danmaku/sign"
)

// App 域接口与网页 passport 是两套体系：这里用设备身份 + 请求签名访问
// 账号数据。签名包与弹幕同源（Apache-2.0），只做只读查询。
const (
	appHost      = "https://api5-normal-sinfonlinea.fqnovel.com"
	appAid       = "8662"
	appVersion   = "7.3.5.32"
	appUserAgent = "com.phoenix.read/73532 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"
)

// ErrNeedsLogin 表示该接口需要登录态才能读取。
var ErrNeedsLogin = errors.New("需要登录后才能读取")

// ShelfItem 是账号收藏或预约里的一条短剧。
type ShelfItem struct {
	SeriesID  string
	Title     string
	Cover     string
	Episodes  int
	Online    bool
	PublishAt string
}

// Device 是 App 域接口要求的设备身份，需要在多次请求间保持一致。
type Device struct {
	DeviceID  string
	InstallID string
	profile   *sdk.DeviceProfile
}

// NewDevice 生成一份设备身份。
func NewDevice() Device {
	return Device{DeviceID: randomDigits(19), InstallID: randomDigits(19)}
}

func randomDigits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(byte('0' + rand.Intn(10)))
	}
	s := b.String()
	if s[0] == '0' {
		s = "1" + s[1:]
	}
	return s
}

// appGet 发起一次带签名的只读请求。cookie 可为空（匿名）。
func (c *Client) appGet(ctx context.Context, path string, pairs [][2]string, dev Device) ([]byte, error) {
	full := make([][2]string, 0, len(pairs)+3)
	full = append(full, [2]string{"aid", appAid}, [2]string{"device_id", dev.DeviceID}, [2]string{"iid", dev.InstallID})
	full = append(full, pairs...)
	query, headers := sign.SignPOST(path, full, time.Now().UnixMilli(), dev.DeviceID, appVersion, nil, nil)
	base := c.appBase
	if base == "" {
		base = appHost
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: 请求构造失败", ErrUpstream)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", appUserAgent)
	req.Header.Set("Accept", "application/json")
	if cookie := c.CookieHeader(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, redact(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w（HTTP %d）", ErrUpstream, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, redact(err))
	}
	return body, nil
}

// appEnvelope 是 App 域接口的统一外层。
type appEnvelope struct {
	Code    json.Number     `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e appEnvelope) check(what string) error {
	code := strings.TrimSpace(e.Code.String())
	if code == "" || code == "0" {
		return nil
	}
	switch code {
	case "101119", "10010", "1009", "8":
		return fmt.Errorf("%s: %w", what, ErrNeedsLogin)
	}
	return fmt.Errorf("%w: %s 失败（%s）", ErrUpstream, what, code)
}

// Favorites 读取账号收藏的短剧。匿名身份下服务端返回空列表。
func (c *Client) Favorites(ctx context.Context, dev Device) ([]ShelfItem, error) {
	body, err := c.appGet(ctx, "/reading/bookapi/bookshelf/video/info/v1/", nil, dev)
	if err != nil {
		return nil, err
	}
	var env appEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%w: 收藏响应无法解析", ErrUpstream)
	}
	if err := env.check("收藏列表"); err != nil {
		return nil, err
	}
	return parseShelf(env.Data)
}

// parseShelf 解析收藏/预约列表。服务端在不同接口里用了不同字段名，
// 这里按已知字段宽松读取，解析不出条目的字段就跳过该条。
func parseShelf(raw json.RawMessage) ([]ShelfItem, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var wrap struct {
		VideoShelfInfo []json.RawMessage `json:"video_shelf_info"`
		SubscribeItems []json.RawMessage `json:"subscribe_items"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, fmt.Errorf("%w: 列表结构无法解析", ErrUpstream)
	}
	entries := append(append([]json.RawMessage{}, wrap.VideoShelfInfo...), wrap.SubscribeItems...)
	out := make([]ShelfItem, 0, len(entries))
	for _, entry := range entries {
		if item, ok := parseShelfEntry(entry); ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func parseShelfEntry(raw json.RawMessage) (ShelfItem, bool) {
	var probe struct {
		SeriesID     json.Number     `json:"series_id"`
		ItemID       json.Number     `json:"item_id"`
		Title        string          `json:"series_title"`
		Title2       string          `json:"title"`
		Name         string          `json:"name"`
		Cover        string          `json:"series_cover_url"`
		Cover2       string          `json:"cover"`
		Episodes     int             `json:"episode_cnt"`
		IsOnline     *bool           `json:"is_online"`
		PublishAt    json.RawMessage `json:"schedule_publish_time"`
		ExpectedAt   json.RawMessage `json:"expected_publish_time"`
		PublishText  string          `json:"sub_info"`
		ExpectedText string          `json:"expected_rec_text"`
		VideoData    *struct {
			SeriesID json.Number `json:"series_id"`
			Title    string      `json:"series_title"`
			Cover    string      `json:"series_cover_url"`
			Episodes int         `json:"episode_cnt"`
		} `json:"video_data"`
		SubscribeData *struct {
			SeriesID    json.Number `json:"series_id"`
			Title       string      `json:"title"`
			SeriesTitle string      `json:"series_title"`
			Cover       string      `json:"cover"`
			SeriesCover string      `json:"series_cover_url"`
			Episodes    int         `json:"episode_cnt"`
		} `json:"subscribe_data"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ShelfItem{}, false
	}
	item := ShelfItem{
		SeriesID:  strings.TrimSpace(probe.SeriesID.String()),
		Title:     firstNonEmpty(probe.Title, probe.Title2, probe.Name),
		Cover:     firstNonEmpty(probe.Cover, probe.Cover2),
		Episodes:  probe.Episodes,
		PublishAt: firstNonEmpty(publishValue(probe.PublishAt), publishValue(probe.ExpectedAt), probe.PublishText, probe.ExpectedText),
	}
	if item.SeriesID == "" {
		item.SeriesID = strings.TrimSpace(probe.ItemID.String())
	}
	if probe.VideoData != nil {
		if id := strings.TrimSpace(probe.VideoData.SeriesID.String()); id != "" {
			item.SeriesID = id
		}
		if item.Episodes == 0 {
			item.Episodes = probe.VideoData.Episodes
		}
		item.Title = firstNonEmpty(item.Title, probe.VideoData.Title)
		item.Cover = firstNonEmpty(item.Cover, probe.VideoData.Cover)
	}
	if probe.SubscribeData != nil {
		if id := strings.TrimSpace(probe.SubscribeData.SeriesID.String()); id != "" {
			item.SeriesID = id
		}
		if item.Title == "" {
			item.Title = firstNonEmpty(probe.SubscribeData.Title, probe.SubscribeData.SeriesTitle)
		}
		if item.Cover == "" {
			item.Cover = firstNonEmpty(probe.SubscribeData.Cover, probe.SubscribeData.SeriesCover)
		}
		if item.Episodes == 0 {
			item.Episodes = probe.SubscribeData.Episodes
		}
	}
	if probe.IsOnline != nil {
		item.Online = *probe.IsOnline
	}
	if item.SeriesID == "" {
		return ShelfItem{}, false
	}
	return item, true
}
