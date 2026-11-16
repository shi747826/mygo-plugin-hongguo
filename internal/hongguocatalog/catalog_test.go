package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
)

func newTestClient(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, dir
}
func TestPersistenceAndDetachedSnapshot(t *testing.T) {
	c, dir := newTestClient(t)
	s := Series{ID: "series", Title: "短剧", Cover: "cover", Description: "描述", Episodes: 12}
	if state, err := c.ToggleFavorite(s); !state || err != nil {
		t.Fatalf("favorite: %v %v", state, err)
	}
	p := Progress{Series: s, Episode: Episode{ID: "ep", Title: "第2集", Number: 2}, Position: 17.25}
	if err := c.SaveProgress(p); err != nil {
		t.Fatal(err)
	}
	first, _ := c.Library()
	if first.History[s.ID].UpdatedAt.IsZero() {
		t.Fatal("missing timestamp")
	}
	delete(first.Favorites, s.ID)
	delete(first.History, s.ID)
	actual, _ := c.Library()
	profileBefore, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	profileAfter, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil || string(profileBefore) != string(profileAfter) {
		t.Fatal("reopening changed anonymous identity", err)
	}
	persisted, _ := reopened.Library()
	if !reflect.DeepEqual(actual, persisted) {
		t.Fatalf("disk/memory differ: %#v %#v", actual, persisted)
	}
	if len(actual.Favorites) != 1 || len(actual.History) != 1 {
		t.Fatal("snapshot changed original maps")
	}
	for _, name := range []string{"library.json", "profile.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions: %v", name, info.Mode())
		}
	}
	if state, err := reopened.ToggleFavorite(s); state || err != nil {
		t.Fatalf("unfavorite: %v %v", state, err)
	}
	next, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := next.Library()
	if len(lib.Favorites) != 0 || len(lib.History) != 1 {
		t.Fatalf("bad unfavorite: %#v", lib)
	}
}
func TestCorruptFilesPreserved(t *testing.T) {
	for _, name := range []string{"library.json", "profile.json"} {
		for _, body := range []string{"{broken", "null", `{"favorites":{"wrong":{"id":"other"}}}`} {
			t.Run(name+body, func(t *testing.T) {
				_, dir := newTestClient(t)
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := New(dir); err == nil {
					t.Fatal("expected corrupt-file error")
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != body {
					t.Fatalf("corrupt file overwritten: %q %v", got, err)
				}
			})
		}
	}
}
func TestFailedWriteKeepsMemoryAndDisk(t *testing.T) {
	c, dir := newTestClient(t)
	before, _ := os.ReadFile(c.path)
	// A missing parent forces a deterministic write error even when tests run as root.
	originalPath := c.path
	c.path = filepath.Join(dir, "missing", "library.json")
	if state, err := c.ToggleFavorite(Series{ID: "new"}); err == nil || state {
		t.Fatalf("write should fail: %v %v", state, err)
	}
	if err := c.SaveProgress(Progress{Series: Series{ID: "new"}, Episode: Episode{ID: "ep", Number: 1}}); err == nil {
		t.Fatal("write should fail")
	}
	lib, _ := c.Library()
	if len(lib.Favorites)+len(lib.History) != 0 {
		t.Fatal("failed write mutated memory")
	}
	after, _ := os.ReadFile(originalPath)
	if string(before) != string(after) {
		t.Fatal("failed write mutated disk")
	}
}
func TestHistoryBoundAndStaleWrites(t *testing.T) {
	c, _ := newTestClient(t)
	now := time.Now().UTC()
	for i := 0; i < historyLimit+3; i++ {
		p := Progress{Series: Series{ID: fmt.Sprint(i)}, Episode: Episode{ID: "ep", Number: 1}, UpdatedAt: now.Add(time.Duration(i) * time.Second)}
		if err := c.SaveProgress(p); err != nil {
			t.Fatal(err)
		}
	}
	lib, _ := c.Library()
	if len(lib.History) != historyLimit {
		t.Fatalf("history size = %d", len(lib.History))
	}
	if _, ok := lib.History["0"]; ok {
		t.Fatal("oldest record retained")
	}
	latest := lib.History["202"]
	stale := latest
	stale.UpdatedAt = now
	stale.Position = 200
	if err := c.SaveProgress(stale); err != nil {
		t.Fatal(err)
	}
	lib, _ = c.Library()
	if lib.History["202"] != latest {
		t.Fatal("stale write replaced latest progress")
	}
}
func TestInvalidInputAndPaths(t *testing.T) {
	if _, err := New(" "); !errors.Is(err, sdk.ErrInvalidOptions) {
		t.Fatal(err)
	}
	c, dir := newTestClient(t)
	if _, err := New(c.path); err == nil {
		t.Fatal("file used as directory")
	}
	if _, err := c.ToggleFavorite(Series{}); err == nil {
		t.Fatal("empty favorite")
	}
	for _, p := range []Progress{
		{},
		{Series: Series{ID: "s"}, Episode: Episode{ID: "e", Number: 0}},
		{Series: Series{ID: "s"}, Episode: Episode{ID: "e", Number: 1}, Position: -1},
		{Series: Series{ID: "s"}, Episode: Episode{ID: "e", Number: 1}, Position: math.NaN()},
		{Series: Series{ID: "s"}, Episode: Episode{ID: "e", Number: 1}, Position: math.Inf(1)},
	} {
		if err := c.SaveProgress(p); err == nil {
			t.Fatalf("invalid progress accepted: %#v", p)
		}
	}
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(link, emptyLibrary()); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestConcurrentMutations(t *testing.T) {
	c, dir := newTestClient(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := c.ToggleFavorite(Series{ID: fmt.Sprint(i)}); err != nil {
				t.Error(err)
			}
			_, _ = c.Library()
		}(i)
	}
	wg.Wait()
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := reopened.Library()
	if len(lib.Favorites) != 20 {
		t.Fatalf("lost updates: %d", len(lib.Favorites))
	}
}

type fakeService struct {
	err      error
	playback *model.Playback
	discover sdk.DiscoverOptions
	search   sdk.SearchOptions
	query    string
	rank     sdk.ShortRankQuery
	// details 覆盖批量详情结果；为空时返回只带 ID 和封面的占位详情。
	details []model.SeriesDetail
	// releases 覆盖上新日历条目。
	releases []model.NewRelease
}

func (f *fakeService) Discover(_ context.Context, o sdk.DiscoverOptions) (*sdk.Page[model.SeriesSummary], error) {
	f.discover = o
	return &sdk.Page[model.SeriesSummary]{Items: []model.SeriesSummary{{ID: "s", Title: "标题", CoverURL: "cover", Description: "描述", EpisodeCount: 3}}, Cursor: "next", HasMore: true}, f.err
}
func (f *fakeService) Search(_ context.Context, q string, o sdk.SearchOptions) (*sdk.Page[model.SeriesSummary], error) {
	f.query, f.search = q, o
	return f.Discover(context.Background(), sdk.DiscoverOptions{})
}
func (f *fakeService) ShortRank(_ context.Context, q sdk.ShortRankQuery) (*model.ShortRanking, error) {
	f.rank = q
	return &model.ShortRanking{Items: []model.RankItem{{SeriesID: "s", Title: "榜单", EpisodeCount: 4}, {ActorID: "actor"}}, Cursor: "rank-next", HasMore: true}, f.err
}
func (f *fakeService) ListEpisodes(context.Context, string, sdk.EpisodeOptions) (*sdk.Page[model.Episode], error) {
	return &sdk.Page[model.Episode]{Items: []model.Episode{{ID: "e", Title: "第一集", Number: 1}}}, f.err
}
func (f *fakeService) ResolvePlayback(context.Context, string, sdk.PlaybackOptions) (*model.Playback, error) {
	return f.playback, f.err
}
func (f *fakeService) GetSeriesBatch(_ context.Context, ids []string) ([]model.SeriesDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.details != nil {
		return f.details, nil
	}
	if len(ids) == 0 {
		return nil, nil
	}
	out := []model.SeriesDetail{}
	for _, id := range ids {
		out = append(out, model.SeriesDetail{Series: &model.Series{SeriesSummary: model.SeriesSummary{ID: id, CoverURL: "cover-" + id}}})
	}
	return out, nil
}
func (f *fakeService) Seasons(_ context.Context, id string) (*model.Album, error) {
	return &model.Album{Seasons: []model.Season{{SeriesID: id, Title: "第一季", Index: 1, CoverURL: "cover", EpisodeCount: 3}, {SeriesID: id + "-2", Title: "第二季", Index: 2, CoverURL: "cover2", EpisodeCount: 5}}}, f.err
}
func (f *fakeService) ResetDevice(context.Context) error { return f.err }
func (f *fakeService) NewReleases(_ context.Context, q sdk.NewReleaseQuery) (*model.NewReleasePage, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.releases != nil {
		return &model.NewReleasePage{Date: q.Date, Items: f.releases}, nil
	}
	return &model.NewReleasePage{Date: q.Date}, nil
}

func TestResetDeviceSurfacesErrors(t *testing.T) {
	c := &Client{api: &fakeService{}}
	if err := c.ResetDevice(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c = &Client{api: &fakeService{err: sdk.ErrTransport}}
	if err := c.ResetDevice(context.Background()); !errors.Is(err, sdk.ErrTransport) {
		t.Fatalf("want transport error, got %v", err)
	}
}

func TestCalendarIndexesReleases(t *testing.T) {
	f := &fakeService{releases: []model.NewRelease{
		{SeriesID: "11", Title: "预告剧", Online: false, Note: "预计10月26日上线"},
		{SeriesID: "12", Title: "已上线", Online: true},
	}}
	c := &Client{api: f}
	got, err := c.Calendar(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if e, ok := got["11"]; !ok || e.Online || e.Note == "" {
		t.Fatalf("entry 11 = %+v", got["11"])
	}
	if e, ok := got["12"]; !ok || !e.Online {
		t.Fatalf("entry 12 = %+v", got["12"])
	}
}

func TestCalendarToleratesPartialFailure(t *testing.T) {
	// 任何一天失败都不应让整体失败：日历只是佐证。
	f := &fakeService{releases: []model.NewRelease{{SeriesID: "11", Online: false}}}
	c := &Client{api: f}
	got, err := c.Calendar(context.Background(), 2)
	if err != nil || len(got) != 1 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestSDKMappingWithoutNetwork(t *testing.T) {
	f := &fakeService{}
	c := &Client{api: f}
	ctx := context.Background()
	page, err := c.Discover(ctx, "推荐", "cursor")
	if err != nil || page.Cursor != "next" || !page.HasMore || page.Items[0].Cover != "cover" || page.Items[0].Episodes != 3 || f.discover.Channel != "推荐" || f.discover.Cursor != "cursor" {
		t.Fatalf("discover: %#v %v", page, err)
	}
	if _, err := c.Search(ctx, " 剧名 ", "search-cursor"); err != nil || f.query != "剧名" || f.search.Cursor != "search-cursor" {
		t.Fatalf("search: %#v %v", f, err)
	}
	rank, err := c.Rank(ctx)
	if err != nil || len(rank.Items) != 1 || rank.Items[0].Episodes != 4 || f.rank.Content != "all" {
		t.Fatalf("rank: %#v %v", rank, err)
	}
	episodes, err := c.Episodes(ctx, "s")
	if err != nil || len(episodes) != 1 || episodes[0].Number != 1 {
		t.Fatalf("episodes: %#v %v", episodes, err)
	}
	f.playback = &model.Playback{Protected: true}
	f.err = sdk.ErrDRMProtected
	pb, err := c.Playback(ctx, "e")
	if pb != f.playback || !errors.Is(err, sdk.ErrDRMProtected) {
		t.Fatalf("DRM model or error lost: %p %v", pb, err)
	}
	for _, cause := range []error{sdk.ErrRateLimited, sdk.ErrAnonymousLimited, sdk.ErrDeviceRejected, sdk.ErrAuthenticationRequired, context.Canceled, context.DeadlineExceeded, errors.New("https://secret?device=secret&key=secret")} {
		f.err = cause
		_, err := c.Discover(ctx, "", "")
		if !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe or unwrapped error: %v", err)
		}
	}
	if _, err := c.Search(ctx, " ", ""); err == nil {
		t.Fatal("empty search")
	}
	if _, err := c.Episodes(ctx, ""); err == nil {
		t.Fatal("empty series")
	}
	if _, err := c.Playback(ctx, ""); err == nil {
		t.Fatal("empty episode")
	}
}
func TestCoversMapsBatchDetails(t *testing.T) {
	f := &fakeService{}
	c := &Client{api: f}
	got, err := c.Covers(context.Background(), []string{"a", "b"})
	if err != nil || got["a"] != "cover-a" || got["b"] != "cover-b" {
		t.Fatalf("covers: %#v %v", got, err)
	}
}
func TestSeasonsMapsAlbum(t *testing.T) {
	f := &fakeService{}
	c := &Client{api: f}
	seasons, err := c.Seasons(context.Background(), "s")
	if err != nil || len(seasons) != 2 || seasons[0].Season != 1 || seasons[1].Season != 2 || seasons[0].Cover != "cover" {
		t.Fatalf("seasons: %#v %v", seasons, err)
	}
	if _, err := c.Seasons(context.Background(), " "); err == nil {
		t.Fatal("empty series")
	}
}

func TestDetailReportsPublishedAndSeason(t *testing.T) {
	f := &fakeService{details: []model.SeriesDetail{{
		Series: &model.Series{
			SeriesSummary: model.SeriesSummary{ID: "s", Title: "第十一季", CoverURL: "cover", Description: "简介", EpisodeCount: 3},
			SeasonIndex:   11, Status: model.SeriesUpdating,
		},
		Episodes: []model.Episode{{ID: "e1", Title: "第1集", Number: 1}, {ID: "e2", Number: 2}},
	}}}
	c := &Client{api: f}
	s, episodes, err := c.Detail(context.Background(), " s ")
	if err != nil || s.ReleaseState != ReleasePublished || s.Season != 11 || s.Episodes != 3 || s.Cover != "cover" || len(episodes) != 2 || episodes[0].ID != "e1" {
		t.Fatalf("detail: %#v %#v %v", s, episodes, err)
	}
	if s.ReleaseNote != "" {
		t.Fatalf("published note: %q", s.ReleaseNote)
	}
}

// 服务端详情成功返回但公开零分集，是唯一可以标记待上线的证据；
// 集数为零只作为其中一个条件，读取失败不产生任何状态。
func TestDetailZeroEpisodesMarksUpcoming(t *testing.T) {
	f := &fakeService{details: []model.SeriesDetail{{
		Series: &model.Series{
			SeriesSummary: model.SeriesSummary{ID: "s", Title: "持械入宋：第十一季"},
			SeasonIndex:   11, Status: model.SeriesUpdating,
			RightText: "更新至0集", Subtitles: []string{"更新至0集"},
		},
	}}}
	c := &Client{api: f}
	s, episodes, err := c.Detail(context.Background(), "s")
	if err != nil || s.ReleaseState != ReleaseUpcoming || s.Episodes != 0 || len(episodes) != 0 || s.ReleaseNote != "更新至0集" {
		t.Fatalf("upcoming: %#v %#v %v", s, episodes, err)
	}
	// 角标缺失时不能假定待上线：零分集也可能因下架或地区限制导致。
	f.details[0].Series.RightText, f.details[0].Series.Subtitles = "", nil
	if s, _, err = c.Detail(context.Background(), "s"); err != nil || s.ReleaseState != ReleaseUnknown || s.ReleaseNote != "" {
		t.Fatalf("note fallback should stay unknown: %#v %v", s, err)
	}
	// 角标带预约语义时仍标记待上线。
	f.details[0].Series.RightText = "159.7万人预约"
	if s, _, err = c.Detail(context.Background(), "s"); err != nil || s.ReleaseState != ReleaseUpcoming {
		t.Fatalf("reservation badge: %#v %v", s, err)
	}
	f.details[0].Series.RightText, f.details[0].Series.Subtitles = "更新至0集", []string{"更新至0集"}
	// 服务端报告有集数、但没公开分集列表时仍是已上线，不能当成待上线。
	f.details[0].Series.EpisodeCount = 6
	if s, _, err = c.Detail(context.Background(), "s"); err != nil || s.ReleaseState != ReleasePublished || s.Episodes != 6 {
		t.Fatalf("count without list: %#v %v", s, err)
	}
}

func TestDetailFailuresDoNotGuessReleaseState(t *testing.T) {
	c := &Client{api: &fakeService{details: []model.SeriesDetail{}}}
	if _, _, err := c.Detail(context.Background(), "missing"); !errors.Is(err, sdk.ErrContentUnavailable) {
		t.Fatalf("missing detail: %v", err)
	}
	if _, _, err := c.Detail(context.Background(), " "); !errors.Is(err, sdk.ErrInvalidOptions) {
		t.Fatal("empty id accepted")
	}
	cause := errors.New("https://secret?device=secret")
	s, episodes, err := (&Client{api: &fakeService{err: cause}}).Detail(context.Background(), "s")
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") || s.ReleaseState != ReleaseUnknown || episodes != nil {
		t.Fatalf("error or state leaked: %#v %#v %v", s, episodes, err)
	}
}

// 旧收藏 JSON 不含发布状态字段，读取后仍是未知，不能被当成待上线。
func TestLegacyLibraryJSONStillLoads(t *testing.T) {
	_, dir := newTestClient(t)
	legacy := `{"favorites":{"s":{"id":"s","title":"旧收藏","episodes":0}},"history":{}}`
	if err := os.WriteFile(filepath.Join(dir, "library.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := c.Library()
	if got := lib.Favorites["s"]; got.ReleaseState != ReleaseUnknown || got.Title != "旧收藏" {
		t.Fatalf("legacy favorite: %#v", got)
	}
}

func TestMergeFavoritesSkipsExisting(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.MergeFavorites([]Series{{ID: "a", Title: "甲"}, {ID: "", Title: "空"}}); err != nil {
		t.Fatal(err)
	}
	added, err := c.MergeFavorites([]Series{{ID: "a", Title: "甲"}, {ID: "b", Title: "乙"}})
	if err != nil || added != 1 {
		t.Fatalf("added=%d err=%v", added, err)
	}
	lib, err := c.Library()
	if err != nil || len(lib.Favorites) != 2 || lib.Favorites["b"].Title != "乙" {
		t.Fatalf("lib=%+v err=%v", lib.Favorites, err)
	}
}
