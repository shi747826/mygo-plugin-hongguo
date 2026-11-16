package catalog

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestReleaseLiveProbe 是显式开启的匿名只读实时探针，只打印公开元数据，
// 不输出 signedURL、设备档案或任何请求地址。默认跳过，避免测试联网。
func TestReleaseLiveProbe(t *testing.T) {
	if os.Getenv("HONGGUO_RELEASE_PROBE") != "1" {
		t.Skip("set HONGGUO_RELEASE_PROBE=1 for an anonymous read-only probe")
	}
	c, _ := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	page, err := c.Search(ctx, "持械入宋", "")
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("search: %v %d", err, len(page.Items))
	}
	seasons, err := c.Seasons(ctx, page.Items[0].ID)
	if err != nil {
		t.Fatal("seasons failed")
	}
	for _, season := range seasons {
		if season.Season != 11 {
			continue
		}
		s, episodes, err := c.Detail(ctx, season.ID)
		t.Logf("season=%d episodes=%d state=%q note=%q err=%v", s.Season, len(episodes), s.ReleaseState, s.ReleaseNote, err)
	}
}
