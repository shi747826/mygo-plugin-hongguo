package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/beilunyang/openhongguo/adapters/filestore"
	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/shi747826/mygo-plugin-hongguo/internal/danmaku/sign"
)

var ErrDeviceUnregistered = errors.New("播放设备尚未初始化")

// LoadReservationDevice reuses the registered catalog device without changing
// credentials or generating unrelated account identities.
func LoadReservationDevice(ctx context.Context, dataDir string) (Device, error) {
	p, err := filestore.New(filepath.Join(dataDir, "profile.json")).Load(ctx)
	if err != nil {
		return Device{}, err
	}
	if p == nil || p.DeviceID == "" || p.InstallID == "" {
		return Device{}, ErrDeviceUnregistered
	}
	return Device{DeviceID: p.DeviceID, InstallID: p.InstallID, profile: p}, nil
}

func reservationPairs(dev Device) [][2]string {
	d := model.DefaultDevice
	cdid, openudid := "", ""
	if dev.profile != nil {
		cdid, openudid = dev.profile.CDID, dev.profile.OpenUDID
		if dev.profile.Device != nil {
			d = dev.profile.Device.OrDefault()
		}
	}
	kind, pad := "phone", "0"
	if d.Pad {
		kind, pad = "pad", "1"
	}
	return [][2]string{
		{"iid", dev.InstallID}, {"device_id", dev.DeviceID}, {"ac", "wifi"}, {"channel", d.Channel},
		{"aid", appAid}, {"app_name", "novelread"}, {"cdid", cdid},
		{"version_code", "73932"}, {"version_name", "7.3.9.32"}, {"device_platform", "android"}, {"os", "android"}, {"ssmix", "a"},
		{"device_type", d.Model}, {"device_brand", d.Brand}, {"language", "zh"}, {"os_api", strconv.Itoa(d.OSAPI)}, {"os_version", d.OSVersion},
		{"manifest_version_code", "73932"}, {"resolution", d.Resolution}, {"dpi", strconv.Itoa(d.DPI)}, {"update_version_code", "73932"},
		{"host_abi", "arm64-v8a"}, {"dragon_device_type", kind}, {"pv_player", "73932"}, {"compliance_status", "0"},
		{"need_personal_recommend", "1"}, {"player_so_load", "1"}, {"is_android_pad_screen", pad}, {"openudid", openudid},
	}
}

func (c *Client) reservationGet(ctx context.Context, dev Device, online bool, offset int, session string) ([]byte, error) {
	if strings.TrimSpace(c.CookieHeader()) == "" {
		return nil, ErrNeedsLogin
	}
	const path = "/reading/user/subscribe/list/v1/"
	pairs := append(reservationPairs(dev), [][2]string{
		{"tab_type", "13"}, {"is_online", strconv.FormatBool(online)}, {"offset", strconv.Itoa(offset)},
		{"limit", "20"}, {"subscribe_offset", "0"}, {"subscribe_order_type", "0"}, {"swipe_type", "0"},
		{"need_calendar_schema", "false"}, {"user_is_login", "1"},
	}...)
	if session != "" {
		pairs = append(pairs, [2]string{"session_id", session})
	}
	query, headers := sign.SignPOST(path, pairs, time.Now().UnixMilli(), dev.DeviceID, "7.3.9.32", nil, nil)
	base := c.appBase
	if base == "" {
		base = appHost
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: 预约请求构造失败", ErrUpstream)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", appUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-SS-DP", appAid)
	req.Header.Set("Passport-Sdk-Version", "6031190")
	cookie := "install_id=" + dev.InstallID
	if dev.profile != nil && dev.profile.TTReq != "" {
		cookie += "; ttreq=" + dev.profile.TTReq
	}
	req.Header.Set("Cookie", cookie+"; "+c.CookieHeader())
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: 预约请求连接失败", ErrUpstream)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: 预约请求（HTTP %d）", ErrUpstream, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil, fmt.Errorf("%w: 预约响应无法读取", ErrUpstream)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("预约读取未获授权: %w", ErrNeedsLogin)
	}
	return body, nil
}

// Reservations reads the complete personal list for one release status. It
// never requests tab_type=5 (the anonymous public release calendar).
func (c *Client) Reservations(ctx context.Context, dev Device, online bool) ([]ShelfItem, error) {
	var out []ShelfItem
	seen := map[string]bool{}
	offset, session := 0, ""
	for page := 0; page < 100; page++ {
		body, err := c.reservationGet(ctx, dev, online, offset, session)
		if err != nil {
			return nil, err
		}
		var env appEnvelope
		if json.Unmarshal(body, &env) != nil || env.Code.String() == "" {
			return nil, fmt.Errorf("%w: 预约响应无法解析", ErrUpstream)
		}
		if err := env.check("预约列表"); err != nil {
			return nil, err
		}
		var data struct {
			Rows       *[]json.RawMessage `json:"subscribe_items"`
			HasMore    bool               `json:"has_more"`
			NextOffset *int               `json:"next_offset"`
			Session    string             `json:"session_id"`
		}
		if json.Unmarshal(env.Data, &data) != nil || data.Rows == nil {
			return nil, fmt.Errorf("%w: 预约列表结构无法解析", ErrUpstream)
		}
		added := 0
		for _, raw := range *data.Rows {
			item, ok := parseShelfEntry(raw)
			if !ok || item.Title == "" {
				return nil, fmt.Errorf("%w: 预约条目无法解析", ErrUpstream)
			}
			item.Online = online
			if !seen[item.SeriesID] {
				out = append(out, item)
				seen[item.SeriesID] = true
				added++
			}
		}
		if !data.HasMore {
			return out, nil
		}
		if len(*data.Rows) == 0 || added == 0 {
			return nil, fmt.Errorf("%w: 预约分页未前进", ErrUpstream)
		}
		next := offset + 20
		if data.NextOffset != nil {
			next = *data.NextOffset
		}
		if next <= offset {
			return nil, fmt.Errorf("%w: 预约分页游标无效", ErrUpstream)
		}
		offset = next
		if data.Session != "" {
			session = data.Session
		}
	}
	return nil, fmt.Errorf("%w: 预约分页超出读取范围", ErrUpstream)
}

// publishValue accepts the service's numeric timestamps and string schedule
// labels. Zero/null means no announced date, not January 1970.
func publishValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var number json.Number
		if json.Unmarshal(raw, &number) != nil {
			return ""
		}
		text = number.String()
	}
	text = strings.TrimSpace(text)
	if text == "0" || text == "" {
		return ""
	}
	return text
}
