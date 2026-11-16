// Package catalog 提供匿名片库访问与本地收藏、观看历史。
package catalog

import (
	"context"
	"errors"
	"fmt"
	"github.com/shi747826/mygo-plugin-hongguo/pluginapi"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/beilunyang/openhongguo/adapters/filestore"
	"github.com/beilunyang/openhongguo/adapters/httptransport"
	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
)

type ReleaseState = pluginapi.ReleaseState

const (
	ReleaseUnknown   = pluginapi.ReleaseUnknown
	ReleaseUpcoming  = pluginapi.ReleaseUpcoming
	ReleasePublished = pluginapi.ReleasePublished
)

type Series = pluginapi.Series
type Episode = pluginapi.Episode
type Page = pluginapi.Page

// Progress.Position 是播放位置，单位秒。UpdatedAt 为零时保存时填入当前时间。
type Progress struct {
	Series    Series    `json:"series"`
	Episode   Episode   `json:"episode"`
	Position  float64   `json:"position"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Library.History 按 Series.ID 索引，每部作品保留最近一次观看位置。
type Library struct {
	Favorites map[string]Series   `json:"favorites"`
	History   map[string]Progress `json:"history"`
}

type service interface {
	Discover(context.Context, sdk.DiscoverOptions) (*sdk.Page[model.SeriesSummary], error)
	Search(context.Context, string, sdk.SearchOptions) (*sdk.Page[model.SeriesSummary], error)
	ShortRank(context.Context, sdk.ShortRankQuery) (*model.ShortRanking, error)
	ListEpisodes(context.Context, string, sdk.EpisodeOptions) (*sdk.Page[model.Episode], error)
	ResolvePlayback(context.Context, string, sdk.PlaybackOptions) (*model.Playback, error)
	GetSeriesBatch(context.Context, []string) ([]model.SeriesDetail, error)
	Seasons(context.Context, string) (*model.Album, error)
	NewReleases(context.Context, sdk.NewReleaseQuery) (*model.NewReleasePage, error)
	ResetDevice(context.Context) error
}

// Client 可被多个 goroutine 使用；同一数据目录应由一个 Client 管理。
type Client struct {
	api     service
	mu      sync.Mutex
	path    string
	library Library
}

// New 打开本地数据；损坏文件返回错误且不会覆盖。创建时不发送网络请求。
func New(dataDir string) (*Client, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("请选择有效的数据目录: %w", sdk.ErrInvalidOptions)
	}
	c := &Client{path: filepath.Join(dataDir, "library.json")}
	if err := c.open(); err != nil {
		return nil, err
	}
	profilePath := filepath.Join(dataDir, "profile.json")
	store := filestore.New(profilePath)
	profile, err := store.Load(context.Background())
	if errors.Is(err, sdk.ErrProfileNotFound) {
		profile, err = sdk.NewAnonymousDeviceProfile(nil, nil)
		if err == nil {
			err = store.Save(context.Background(), profile)
		}
	}
	if err != nil {
		return nil, localError("匿名档案读取或保存失败，请备份 profile.json 后检查文件或权限", err)
	}
	if profile == nil || profile.CDID == "" || profile.OpenUDID == "" || profile.ClientUDID == "" || profile.OAID == "" || profile.ReqID == "" {
		return nil, localError("匿名档案内容损坏，请备份 profile.json 后修复；不会自动更换设备", sdk.ErrInvalidOptions)
	}
	if err := privateExisting(profilePath); err != nil {
		return nil, localError("无法保护匿名档案，请检查目录权限", err)
	}
	api, err := sdk.NewClient(sdk.Options{Transport: httptransport.New(), ProfileStore: store, DeviceResets: sdk.Int(0), RetryAttempts: 1})
	if err != nil {
		return nil, localError("片库初始化失败，请检查客户端配置", err)
	}
	c.api = api
	return c, nil
}

func (c *Client) Discover(ctx context.Context, channel, cursor string) (Page, error) {
	p, err := c.api.Discover(ctx, sdk.DiscoverOptions{Channel: channel, Cursor: cursor})
	if err != nil {
		return Page{}, remoteError("加载发现页", err)
	}
	return mapPage(p), nil
}
func (c *Client) Search(ctx context.Context, query, cursor string) (Page, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Page{}, fmt.Errorf("请输入搜索关键词: %w", sdk.ErrInvalidOptions)
	}
	// 一次请求更长的页，减少「加载更多」；服务端仍可能返回较少的结果。
	p, err := c.api.Search(ctx, query, sdk.SearchOptions{Cursor: cursor, PageSize: 50})
	if err != nil {
		return Page{}, remoteError("搜索短剧", err)
	}
	return mapPage(p), nil
}

// Covers 批量补全片库对象的封面地址，用于搜索等服务端不返回封面的路径。
// 不存在的 ID 会被略去，返回按请求顺序排列、含 CoverURL 的封面信息。
func (c *Client) Covers(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	details, err := c.api.GetSeriesBatch(ctx, ids)
	if err != nil {
		return nil, remoteError("加载封面", err)
	}
	for _, d := range details {
		if d.Series != nil && d.Series.CoverURL != "" {
			out[d.Series.ID] = d.Series.CoverURL
		}
	}
	return out, nil
}

// Seasons 返回一部短剧所属合集的全部季，按季序排列；单季作品返回自身一季。
func (c *Client) Seasons(ctx context.Context, seriesID string) ([]Series, error) {
	if strings.TrimSpace(seriesID) == "" {
		return nil, fmt.Errorf("请选择要查看的短剧: %w", sdk.ErrInvalidOptions)
	}
	album, err := c.api.Seasons(ctx, seriesID)
	if err != nil {
		return nil, remoteError("加载剧集季列表", err)
	}
	out := []Series{}
	if album == nil {
		return out, nil
	}
	for _, s := range album.Seasons {
		if s.SeriesID == "" {
			continue
		}
		out = append(out, Series{ID: s.SeriesID, Title: s.Title, Cover: s.CoverURL, Episodes: s.EpisodeCount, Season: s.Index})
	}
	return out, nil
}

// Detail 读取一部短剧的详情与分集，并给出可信的发布状态。
// 它与 Episodes 的区别是不会把「服务端公开零分集」当成匿名访问受限：
// 详情成功返回而分集列表为空时，分集列表为空、ReleaseState 为 ReleaseUpcoming，
// ReleaseNote 带上服务端角标文案（如「更新至0集」）。作品已下架或服务端不返回
// 该 ID 时返回 ErrContentUnavailable；网络、限流和设备错误照常返回，且不猜测状态。
// 返回的 Series 已填写 Season，界面可用传入列表项的标题等覆盖展示字段。
func (c *Client) Detail(ctx context.Context, seriesID string) (Series, []Episode, error) {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return Series{}, nil, fmt.Errorf("请选择要查看的短剧: %w", sdk.ErrInvalidOptions)
	}
	details, err := c.api.GetSeriesBatch(ctx, []string{seriesID})
	if err != nil {
		return Series{}, nil, remoteError("加载剧集详情", err)
	}
	var detail *model.Series
	var listed []model.Episode
	for _, d := range details {
		if d.Series != nil && d.Series.ID == seriesID {
			detail, listed = d.Series, d.Episodes
			break
		}
	}
	if detail == nil {
		return Series{}, nil, localError("加载剧集详情失败，内容可能已下架或暂不可用", sdk.ErrContentUnavailable)
	}
	episodes := make([]Episode, 0, len(listed))
	for _, e := range listed {
		episodes = append(episodes, Episode{ID: e.ID, Title: e.Title, Number: e.Number})
	}
	out := Series{
		ID: detail.ID, Title: detail.Title, Cover: detail.CoverURL, Description: detail.Description,
		Episodes: detail.EpisodeCount, Season: detail.SeasonIndex,
	}
	if out.Episodes == 0 {
		out.Episodes = len(episodes)
	}
	if len(episodes) > 0 || out.Episodes > 0 {
		out.ReleaseState = ReleasePublished
	} else {
		// 零分集本身不是「未开播」的充分条件：下架、地区限制、付费墙或
		// 服务端临时空列表都会返回success+零分集。只有服务端角标给出明确
		// 的未开播语义时才标记待上线，其余保持未知，由界面显示中性提示。
		note := releaseNote(detail)
		out.ReleaseNote = note
		if upcomingNote(note) {
			out.ReleaseState = ReleaseUpcoming
		}
	}
	return out, episodes, nil
}

// upcomingNote 判断服务端角标是否表达了「尚未开播」。宁可漏标，也不把
// 读取异常或空列表误标成待上线。
func upcomingNote(note string) bool {
	compact := strings.ReplaceAll(strings.ReplaceAll(note, " ", ""), "　", "")
	if compact == "" {
		return false
	}
	lower := strings.ToLower(compact)
	for _, keyword := range []string{"预约", "待上线", "即将上线", "即将开播", "敬请期待", "未开播", "尚未开播", "comingsoon"} {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	// 「更新至0集」是服务端表达尚未开播的既有写法；「更新至N集」(N>0)
	// 不走这里，那一类已在上面按已上线处理。
	return strings.Contains(compact, "更新至0集")
}

// releaseNote 取服务端自己的状态文案，避免界面自行编造上线时间。
func releaseNote(detail *model.Series) string {
	if detail == nil {
		return ""
	}
	if note := strings.TrimSpace(detail.RightText); note != "" {
		return note
	}
	for _, s := range detail.Subtitles {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// Rank 读取全部内容的服务端默认短剧榜。
func (c *Client) Rank(ctx context.Context) (Page, error) {
	p, err := c.api.ShortRank(ctx, sdk.ShortRankQuery{Content: "all"})
	if err != nil {
		return Page{}, remoteError("加载排行榜", err)
	}
	out := Page{Items: []Series{}}
	if p == nil {
		return out, nil
	}
	out.Cursor, out.HasMore = p.Cursor, p.HasMore
	for _, s := range p.Items {
		if s.SeriesID != "" {
			out.Items = append(out.Items, Series{ID: s.SeriesID, Title: s.Title, Cover: s.CoverURL, Description: s.Score, Episodes: s.EpisodeCount})
		}
	}
	return out, nil
}
func (c *Client) Episodes(ctx context.Context, seriesID string) ([]Episode, error) {
	if strings.TrimSpace(seriesID) == "" {
		return nil, fmt.Errorf("请选择要观看的短剧: %w", sdk.ErrInvalidOptions)
	}
	p, err := c.api.ListEpisodes(ctx, seriesID, sdk.EpisodeOptions{})
	if err != nil {
		return nil, remoteError("加载分集", err)
	}
	out := []Episode{}
	if p != nil {
		for _, e := range p.Items {
			out = append(out, Episode{ID: e.ID, Title: e.Title, Number: e.Number})
		}
	}
	return out, nil
}

// Playback 保留 SDK 播放模型，包括与 ErrDRMProtected 一同返回的模型。
func (c *Client) Playback(ctx context.Context, episodeID string) (*model.Playback, error) {
	if strings.TrimSpace(episodeID) == "" {
		return nil, fmt.Errorf("请选择要播放的分集: %w", sdk.ErrInvalidOptions)
	}
	p, err := c.api.ResolvePlayback(ctx, episodeID, sdk.PlaybackOptions{})
	if err != nil {
		return p, remoteError("获取播放信息", err)
	}
	return p, nil
}

// ResetDevice 丢弃当前匿名设备并注册一台新的，用于服务端限制匿名播放后
// 主动换一台再试；失败时向调用方返回可分类的错误。
func (c *Client) ResetDevice(ctx context.Context) error {
	if err := c.api.ResetDevice(ctx); err != nil {
		return remoteError("更换匿名设备", err)
	}
	return nil
}

func mapPage(p *sdk.Page[model.SeriesSummary]) Page {
	out := Page{Items: []Series{}}
	if p == nil {
		return out
	}
	out.Cursor, out.HasMore = p.Cursor, p.HasMore
	for _, s := range p.Items {
		out.Items = append(out.Items, Series{ID: s.ID, Title: s.Title, Cover: s.CoverURL, Description: s.Description, Episodes: s.EpisodeCount})
	}
	return out
}

// operationError 保留错误链，但不把 SDK 错误里可能包含的地址或设备信息显示给用户。
type operationError struct {
	message string
	cause   error
}

func (e *operationError) Error() string { return e.message }
func (e *operationError) Unwrap() error { return e.cause }
func localError(message string, err error) error {
	return &operationError{message: message, cause: err}
}
func remoteError(action string, err error) error {
	hint := "失败，请检查网络后重试"
	switch {
	case errors.Is(err, context.Canceled):
		hint = "已取消"
	case errors.Is(err, context.DeadlineExceeded):
		hint = "超时，请稍后重试"
	case errors.Is(err, sdk.ErrRateLimited):
		hint = "受到限流，请稍后再试"
	case errors.Is(err, sdk.ErrDeviceRejected), errors.Is(err, sdk.ErrAnonymousLimited):
		hint = "受到匿名访问限制，自动恢复次数已用完，请稍后再试"
	case errors.Is(err, sdk.ErrAuthenticationRequired):
		hint = "需要登录，当前匿名访问无法继续"
	case errors.Is(err, sdk.ErrChallengeRequired):
		hint = "需要服务端验证，请稍后再试"
	case errors.Is(err, sdk.ErrContentUnavailable):
		hint = "失败，内容可能已下架或暂不可用"
	case errors.Is(err, sdk.ErrDRMProtected):
		hint = "返回加密播放信息，请交由播放器处理"
	case errors.Is(err, sdk.ErrParamInvalid):
		hint = "失败，请重新选择内容或刷新列表"
	}
	return localError(action+hint, err)
}

// UpcomingEntry 是上新日历里的一个条目：已上线，或预告在 PublishAt 上线。
type UpcomingEntry = pluginapi.UpcomingEntry

// Calendar 读取未来若干天的公开上新日历，按短剧 ID 建索引。它是匿名公开
// 日历，不是个人预约列表，只用于给「待上线」判定提供第二重证据。
func (c *Client) Calendar(ctx context.Context, days int) (map[string]UpcomingEntry, error) {
	if days <= 0 {
		days = 7
	}
	if days > 15 {
		days = 15
	}
	out := make(map[string]UpcomingEntry, days*8)
	var firstErr error
	today := time.Now()
	for i := 0; i < days; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		date := today.AddDate(0, 0, i).Format("20060102")
		page, err := c.api.NewReleases(ctx, sdk.NewReleaseQuery{Date: date})
		if err != nil {
			if firstErr == nil {
				firstErr = remoteError("读取上新日历", err)
			}
			continue
		}
		if page == nil {
			continue
		}
		for _, item := range page.Items {
			if item.SeriesID == "" {
				continue
			}
			entry := UpcomingEntry{SeriesID: item.SeriesID, Title: item.Title, Online: item.Online, PublishAt: item.PublishAt, Note: item.Note}
			// 同一天里已上线的证据优先于预告。
			if prev, ok := out[item.SeriesID]; ok && prev.Online {
				continue
			}
			out[item.SeriesID] = entry
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}
