package danmaku

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/adapters/httptransport"
	"github.com/beilunyang/openhongguo/adapters/memstore"
	"github.com/beilunyang/openhongguo/sdk"
)

// TestLiveProbe 匿名只读地验证真实接口。默认跳过：普通 go test 不发网络请求。
// 显式运行：
//
//	HONGGUO_LIVE_PROBE=1 go test ./internal/danmaku -run TestLiveProbe -v
//
// 也可用 HONGGUO_LIVE_SERIES / HONGGUO_LIVE_EPISODE 直接指定 ID，跳过发现步骤。
// 输出只含分集 ID、条数和少量弹幕文本，不含签名、query、URL 或设备档案。
func TestLiveProbe(t *testing.T) {
	if os.Getenv("HONGGUO_LIVE_PROBE") != "1" {
		t.Skip("set HONGGUO_LIVE_PROBE=1 to run the anonymous read probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	series := os.Getenv("HONGGUO_LIVE_SERIES")
	episode := os.Getenv("HONGGUO_LIVE_EPISODE")
	if series == "" || episode == "" {
		api, err := sdk.NewClient(sdk.Options{
			Transport: httptransport.New(), ProfileStore: memstore.New(),
			DeviceResets: sdk.Int(0), RetryAttempts: 1,
		})
		if err != nil {
			t.Fatalf("client init failed: %v", err)
		}
		page, err := api.Search(ctx, "全球死考", sdk.SearchOptions{PageSize: 1})
		if err != nil || len(page.Items) == 0 {
			t.Fatalf("anonymous search failed (details withheld): %v", err != nil)
		}
		eps, err := api.ListEpisodes(ctx, page.Items[0].ID, sdk.EpisodeOptions{})
		if err != nil || len(eps.Items) == 0 {
			t.Fatalf("anonymous episode lookup failed (details withheld): %v", err != nil)
		}
		series, episode = page.Items[0].ID, eps.Items[0].ID
	}

	r, err := New(Options{PageSize: 90, MaxCues: 20, MaxPages: 3, RequestTimeout: 20 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cues, err := r.Fetch(ctx, series, episode)
	if err != nil {
		t.Fatalf("live fetch failed (classified): %v", err)
	}
	t.Logf("series=%s episode=%s cues=%d", series, episode, len(cues))
	for i, c := range cues {
		if i >= 3 {
			break
		}
		t.Logf("  cue %.3fs %q", c.Time, c.Text)
	}
	if len(cues) == 0 {
		t.Fatal("live endpoint returned no danmaku for a known episode")
	}
	for i := 1; i < len(cues); i++ {
		if cues[i].Time < cues[i-1].Time {
			t.Fatalf("live cues are not sorted: %v then %v", cues[i-1].Time, cues[i].Time)
		}
	}
}
