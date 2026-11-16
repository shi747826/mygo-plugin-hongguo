// Package danmaku 读取红果短剧的官方分时弹幕（带视频时间轴的弹幕），
// 与官网匿名页面上那 15 条「本集评论」无关。
//
// 接口不是公开接口：它要求官方 Android 客户端的签名头。本包用内置的
// sign 子包（Apache-2.0，见 sign/NOTICE.md）生成这些头，因此调用方不需要
// 自己实现签名，也不应该看到任何签名或设备标识。所有错误信息都经过裁剪，
// 绝不含请求 URL、query、签名头或设备档案。
//
// 上游随时可能改算法或封禁，调用方必须准备降级路径：把错误当成「读不到」，
// 不要当成「本集没有弹幕」。
package danmaku

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shi747826/mygo-plugin-hongguo/internal/danmaku/sign"
)

// Cue 是一条弹幕。Time 是相对本集开头的秒数（与播放器进度同一坐标系），
// Text 已清洗（控制字符换成空格、去掉首尾空白），按时间升序返回。
type Cue struct {
	Time float64
	Text string
}

var (
	// ErrInvalidOptions 表示调用参数不合法（ID 不是数字、上限为负等）。
	ErrInvalidOptions = errors.New("danmaku: invalid options")
	// ErrRejected 表示上游明确拒绝了请求（非 0 的 code、非 JSON、非 2xx）。
	ErrRejected = errors.New("danmaku: comment endpoint rejected the request")
	// ErrPartial 表示分页中途失败，返回值仍带着已经读到的弹幕。
	ErrPartial = errors.New("danmaku: partial result")
	// ErrRequestFailed 表示连不上或读超时；错误文本不含 URL。
	ErrRequestFailed = errors.New("danmaku: request failed")
)

const (
	// DefaultBaseURL 是官方客户端发弹幕请求的主机。
	DefaultBaseURL = "https://api5-normal-sinfonlineb.fqnovel.com"
	// commentPath 的 %s 是本集数字 ID（group_id）。
	commentPath = "/novel/commentapi/comment/list/%s/v1/"

	// 与官方客户端一致：UA 与签名参数必须成套，不能只改一个。
	defaultUserAgent = "com.phoenix.read/73532 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"

	// 上游按匿名标识分配弹幕子集，所以同一份标识要跨调用复用。
	defaultMaxCues   = 3000
	defaultMaxPages  = 40
	defaultPageSize  = 90
	maxTextLen       = 200              // 超长文本直接丢弃（正常弹幕不会这么长）
	maxTimeSeconds   = 7 * 24 * 60 * 60 // 超过 7 天视为脏数据
	maxResponseBytes = 8 << 20          // 单页响应上限
	maxEmptyPages    = 2                // 连续空页就停：上游偶尔 has_more=true 却一直给空
	windowMillis     = 30 * 1000        // 实测服务端按 30 秒窗口返回
)

// Identity 是发给上游的匿名设备标识。它决定上游给哪一份弹幕子集，
// 因此应当与本地匿名档案一起保存并复用，而不是每次重新生成。
type Identity struct {
	DeviceID  string
	InstallID string
	CDID      string
	OpenUDID  string
}

// Options 配置 Reader。零值字段取默认值；测试用 HTTPClient 与 BaseURL。
type Options struct {
	// BaseURL 为空时用 DefaultBaseURL；测试指向 httptest 服务器。
	BaseURL string
	// HTTPClient 为空时使用带 RequestTimeout 的客户端。传入的客户端
	// 不应自行设置更长的超时：取消仍由 ctx 控制。
	HTTPClient *http.Client
	// Identity 为 nil 时，Reader 在生命周期内生成并复用一份随机标识。
	// 想跨进程保持同一份弹幕子集时，由调用方持久化后传入。
	Identity *Identity
	// UserAgent 为空时使用官方客户端的 UA。
	UserAgent string
	// MaxCues 是单集最多返回的条数，默认 3000；MaxPages 是分页上限，默认 40；
	// PageSize 是单页请求条数，默认 90。三者都必须为正。
	MaxCues  int
	MaxPages int
	PageSize int
	// RequestTimeout 是单页请求的超时，默认 20 秒。
	RequestTimeout time.Duration
	// PageGap 是相邻分页的间隔，默认 120 毫秒：别把上游打急。
	PageGap time.Duration
	// Now 只在测试里替换时钟。
	Now func() time.Time
	// Random 只在测试里替换 nonce / 标识来源。
	Random io.Reader
}

// Reader 可以并发使用。
type Reader struct {
	base     string
	client   *http.Client
	ua       string
	maxCues  int
	maxPages int
	pageSize int
	timeout  time.Duration
	pageGap  time.Duration
	now      func() time.Time

	mu       sync.Mutex
	identity Identity
	identErr error
}

// New 校验并固化配置；不发送网络请求，也不写磁盘。
func New(opts Options) (*Reader, error) {
	r := &Reader{
		base:     strings.TrimRight(opts.BaseURL, "/"),
		client:   opts.HTTPClient,
		ua:       opts.UserAgent,
		maxCues:  opts.MaxCues,
		maxPages: opts.MaxPages,
		pageSize: opts.PageSize,
		timeout:  opts.RequestTimeout,
		pageGap:  opts.PageGap,
		now:      opts.Now,
	}
	if r.base == "" {
		r.base = DefaultBaseURL
	}
	if r.client == nil {
		if r.timeout <= 0 {
			r.timeout = 20 * time.Second
		}
		r.client = &http.Client{Timeout: r.timeout}
	}
	if r.ua == "" {
		r.ua = defaultUserAgent
	}
	if r.maxCues == 0 {
		r.maxCues = defaultMaxCues
	}
	if r.maxPages == 0 {
		r.maxPages = defaultMaxPages
	}
	if r.pageSize == 0 {
		r.pageSize = defaultPageSize
	}
	if r.maxCues < 0 || r.maxPages < 0 || r.pageSize < 0 {
		return nil, fmt.Errorf("上限不能为负: %w", ErrInvalidOptions)
	}
	if r.pageGap < 0 || r.timeout < 0 {
		return nil, fmt.Errorf("时长不能为负: %w", ErrInvalidOptions)
	}
	if r.pageGap == 0 {
		r.pageGap = 120 * time.Millisecond
	}
	if r.now == nil {
		r.now = time.Now
	}
	if opts.Identity != nil {
		r.identity = *opts.Identity
	}
	return r, nil
}

// Fetch 读取一集的有界弹幕列表，按时间升序返回。
//
// seriesID 是短剧 ID（business_param.book_id），episodeID 是本集数字 ID
// （group_id，也就是 catalog.Episode.ID；不是 Playback.VideoID 那种
// "v02ebeg..." 字符串）。实测两者都是 19 位数字，填错会被上游拒绝或拿到空列表。
//
// ctx 取消 / 超时会立刻停止；无论上游还有多少页，最多返回 MaxCues 条、
// 最多翻 MaxPages 页。返回非空弹幕时 error 为 nil；分页中途失败会返回
// 已读到的弹幕并带 ErrPartial。空列表 + nil error 才表示「本集没有弹幕」。
func (r *Reader) Fetch(ctx context.Context, seriesID, episodeID string) ([]Cue, error) {
	if err := validID(seriesID); err != nil {
		return nil, fmt.Errorf("短剧 ID: %w", err)
	}
	if err := validID(episodeID); err != nil {
		return nil, fmt.Errorf("分集 ID: %w", err)
	}
	id, err := r.ident()
	if err != nil {
		return nil, err
	}

	cues := []Cue{}
	seen := map[string]struct{}{}
	var start int64
	emptyPages := 0

	for page := 0; page < r.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return finish(cues, err)
		}
		if page > 0 && r.pageGap > 0 {
			timer := time.NewTimer(r.pageGap)
			select {
			case <-ctx.Done():
				timer.Stop()
				return finish(cues, ctx.Err())
			case <-timer.C:
			}
		}

		pageCues, next, hasMore, err := r.requestPage(ctx, id, seriesID, episodeID, start)
		if err != nil {
			if len(cues) == 0 {
				return nil, err
			}
			return cues, fmt.Errorf("%w: %w", ErrPartial, err)
		}
		for _, c := range pageCues {
			key := fmt.Sprintf("%.3f|%s", c.Time, c.Text)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			cues = append(cues, c)
			if len(cues) >= r.maxCues {
				break
			}
		}
		if len(cues) >= r.maxCues {
			break
		}
		if len(pageCues) > 0 {
			emptyPages = 0
		} else {
			emptyPages++
		}
		if next == nil {
			if hasMore {
				// 上游说还有，但没给游标：停止，让调用方知道这次不完整。
				return sorted(cues), fmt.Errorf("%w: 分页游标缺失", ErrPartial)
			}
			break
		}
		if *next <= start || *next > maxTimeSeconds*1000 || emptyPages >= maxEmptyPages {
			break
		}
		start = *next
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Time < cues[j].Time })
	return cues, nil
}

// ident 返回本次进程复用的匿名标识。
func (r *Reader) ident() (Identity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.identErr != nil {
		return Identity{}, r.identErr
	}
	if r.identity.DeviceID != "" && r.identity.InstallID != "" {
		return r.identity, nil
	}
	device, err := newDeviceID()
	if err != nil {
		r.identErr = fmt.Errorf("无法生成本机标识: %w", ErrRequestFailed)
		return Identity{}, r.identErr
	}
	install, err := newDeviceID()
	if err != nil {
		r.identErr = fmt.Errorf("无法生成本机标识: %w", ErrRequestFailed)
		return Identity{}, r.identErr
	}
	if r.identity.DeviceID == "" {
		r.identity.DeviceID = device
	}
	if r.identity.InstallID == "" {
		r.identity.InstallID = install
	}
	return r.identity, nil
}

// requestPage 请求一个 30 秒时间窗口。签名与 query 在同一处生成：
// 签名覆盖 query，query 里含 _rticket，而 X-Khronos 用秒；三者不一致就是无效签名。
func (r *Reader) requestPage(ctx context.Context, id Identity, seriesID, episodeID string, start int64) ([]Cue, *int64, bool, error) {
	body, err := json.Marshal(map[string]any{
		"comment_source":    601,
		"server_channel":    1000,
		"group_id":          episodeID,
		"group_type":        30,
		"comment_type":      20,
		"sort":              1,
		"count":             r.pageSize,
		"cursor":            "",
		"aid":               8662,
		"compliance_status": 0,
		"business_param": map[string]any{
			"book_id":                 seriesID,
			"start_offset_time":       start,
			"playlet_item_duration":   3600000,
			"need_danmaku_guide_type": []int{1, 3, 4, 2},
		},
	})
	if err != nil {
		return nil, nil, false, fmt.Errorf("请求体编码失败: %w", err)
	}
	path := fmt.Sprintf(commentPath, url.PathEscape(episodeID))
	pairs := [][2]string{
		{"aid", "8662"},
		{"app_name", "novelread"},
		{"version_code", "73532"},
		{"version_name", "7.3.5.32"},
		{"manifest_version_code", "73532"},
		{"update_version_code", "73532"},
		{"channel", "update_64"},
		{"device_platform", "android"},
		{"os", "android"},
		{"ssmix", "a"},
		{"device_type", "25053RT47C"},
		{"device_brand", "Redmi"},
		{"language", "zh"},
		{"os_api", "36"},
		{"os_version", "16"},
		{"resolution", "1280*2772"},
		{"dpi", "520"},
		{"ac", "wifi"},
		{"device_id", id.DeviceID},
		{"iid", id.InstallID},
	}
	if id.CDID != "" {
		pairs = append(pairs, [2]string{"cdid", id.CDID})
	}
	if id.OpenUDID != "" {
		pairs = append(pairs, [2]string{"openudid", id.OpenUDID})
	}
	query, headers := sign.SignPOST(path, pairs, r.now().UnixMilli(), id.DeviceID, "7.3.5.32", body, nil)

	endpoint := r.base + path + "?" + query
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		// 错误里绝不能带 endpoint：query 含 device_id 和签名。
		return nil, nil, false, fmt.Errorf("构造请求失败: %w", ErrRequestFailed)
	}
	req.Header.Set("User-Agent", r.ua)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
		}
		return nil, nil, false, fmt.Errorf("连接弹幕接口失败: %w", ErrRequestFailed)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
		}
		return nil, nil, false, fmt.Errorf("读取弹幕响应失败: %w", ErrRequestFailed)
	}
	if len(raw) > maxResponseBytes {
		return nil, nil, false, fmt.Errorf("响应过大: %w", ErrRejected)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, false, fmt.Errorf("HTTP %d: %w", resp.StatusCode, ErrRejected)
	}

	var payload struct {
		Code json.Number `json:"code"`
		Data *struct {
			List  []json.RawMessage `json:"data_list"`
			Extra *struct {
				Next json.Number `json:"next_query_danmaku_list_time"`
			} `json:"extra"`
			Info *struct {
				HasMore bool            `json:"has_more"`
				Cursor  json.RawMessage `json:"cursor"`
			} `json:"common_list_info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, nil, false, fmt.Errorf("响应不是 JSON: %w", ErrRejected)
	}
	if payload.Code.String() != "0" || payload.Data == nil {
		return nil, nil, false, fmt.Errorf("上游 code=%s: %w", payload.Code.String(), ErrRejected)
	}

	var next *int64
	if payload.Data.Extra != nil {
		if v, err := payload.Data.Extra.Next.Int64(); err == nil && v > 0 {
			next = &v
		}
	}
	hasMore := false
	if payload.Data.Info != nil {
		hasMore = payload.Data.Info.HasMore
		if next == nil {
			if v := cursorEnd(payload.Data.Info.Cursor); v > 0 {
				next = &v
			}
		}
	}
	if next != nil && *next <= 0 {
		next = nil
	}

	cues := make([]Cue, 0, len(payload.Data.List))
	for _, row := range payload.Data.List {
		if c, ok := parseRow(row, episodeID); ok {
			cues = append(cues, c)
		}
	}
	return cues, next, hasMore, nil
}

// row 只解出需要的字段：别的字段（用户 id、互动统计）与弹幕无关。
type row struct {
	Comment struct {
		Common struct {
			GroupID string      `json:"group_id"`
			Status  json.Number `json:"status"`
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"common"`
		Expand struct {
			OffsetTime *json.Number `json:"offset_time"`
		} `json:"expand"`
		OffsetTime *json.Number `json:"offset_time"`
	} `json:"comment"`
}

// parseRow 清洗一行；返回 false 表示这行不该出现在弹幕列表里。
func parseRow(raw json.RawMessage, episodeID string) (Cue, bool) {
	var r row
	if err := json.Unmarshal(raw, &r); err != nil {
		return Cue{}, false
	}
	// 只收本视频、且状态正常的弹幕：别的值会把待审核/已折叠的脏数据混进来。
	if g := strings.TrimSpace(r.Comment.Common.GroupID); g != "" && g != episodeID {
		return Cue{}, false
	}
	if r.Comment.Common.Status.String() != "1" {
		return Cue{}, false
	}
	text := cleanText(r.Comment.Common.Content.Text)
	if text == "" || len([]rune(text)) > maxTextLen {
		return Cue{}, false
	}
	ms := r.Comment.Expand.OffsetTime
	if ms == nil {
		ms = r.Comment.OffsetTime
	}
	if ms == nil {
		return Cue{}, false
	}
	v, err := ms.Float64()
	if err != nil || v < 0 || v > maxTimeSeconds*1000 {
		return Cue{}, false
	}
	return Cue{Time: v / 1000, Text: text}, true
}

// cleanText 把控制字符与行分隔符换成空格，避免污染渲染层。
func cleanText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		if c < 0x20 || c == 0x7f || c == '\u2028' || c == '\u2029' {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(c)
	}
	return strings.TrimSpace(b.String())
}

// cursorEnd 从 common_list_info.cursor 里取分页游标；它可能是字符串化的 JSON。
func cursorEnd(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var obj struct {
		End json.Number `json:"end_offset_time"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if v, err := obj.End.Int64(); err == nil && v > 0 {
			return v
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && strings.HasPrefix(strings.TrimSpace(s), "{") {
		return cursorEnd(json.RawMessage(strings.TrimSpace(s)))
	}
	return 0
}

func validID(id string) error {
	t := strings.TrimSpace(id)
	if len(t) < 6 || len(t) > 30 {
		return fmt.Errorf("需要 6-30 位数字: %w", ErrInvalidOptions)
	}
	for _, c := range t {
		if c < '0' || c > '9' {
			return fmt.Errorf("需要纯数字: %w", ErrInvalidOptions)
		}
	}
	return nil
}

// newDeviceID 生成 19 位数字标识，与官方客户端的 anonymous id 形态一致。
func newDeviceID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	n := new(big.Int).SetUint64(binary.LittleEndian.Uint64(buf[:]))
	n.Mod(n, big.NewInt(8000000000000000000))
	n.Add(n, big.NewInt(1000000000000000000))
	return n.String(), nil
}

func finish(cues []Cue, err error) ([]Cue, error) {
	if len(cues) == 0 {
		return nil, err
	}
	return sorted(cues), fmt.Errorf("%w: %w", ErrPartial, err)
}

func sorted(cues []Cue) []Cue {
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Time < cues[j].Time })
	return cues
}
