package danmaku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testSeries = "7697459724635098174"
	testVideo  = "7697465535398677529"
)

// fixture 是一个可按分页窗口返回固定数据的假上游。
type fixture struct {
	mu     sync.Mutex
	starts []int64
	bodies []map[string]any
	pages  map[int64]string // start_offset_time -> 响应
	code   string
	block  chan struct{} // 非 nil 时，请求会一直等到 ctx 结束或 close
	hasHdr bool
}

func (f *fixture) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		f.mu.Lock()
		if f.block != nil {
			block := f.block
			f.mu.Unlock()
			select {
			case <-req.Context().Done():
				return
			case <-block:
			}
			f.mu.Lock()
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			f.mu.Unlock()
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.bodies = append(f.bodies, body)
		if req.Header.Get("X-Gorgon") != "" && req.Header.Get("X-Argus") != "" && req.Header.Get("X-Ladon") != "" {
			f.hasHdr = true
		}
		param, _ := body["business_param"].(map[string]any)
		start, _ := param["start_offset_time"].(float64)
		f.starts = append(f.starts, int64(start))
		page, ok := f.pages[int64(start)]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "unexpected start", http.StatusBadRequest)
			return
		}
		if f.code != "" {
			fmt.Fprintf(w, `{"code":%s,"message":"no","data":{"data_list":[]}}`, f.code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, page)
	})
}

func fixtureRow(video, text string, status int, offsetMS int) string {
	return fmt.Sprintf(`{"comment":{"common":{"group_id":%q,"status":%d,"content":{"text":%q}},"expand":{"offset_time":%d}}}`,
		video, status, text, offsetMS)
}

func page(next int, more bool, rows ...string) string {
	extra := "{}"
	cursor := "{}"
	if next > 0 {
		extra = fmt.Sprintf(`{"next_query_danmaku_list_time":%d}`, next)
		cursor = fmt.Sprintf(`{"end_offset_time":%d}`, next)
	}
	return fmt.Sprintf(`{"code":0,"data":{"data_list":[%s],"extra":%s,"common_list_info":{"has_more":%v,"cursor":%q}}}`,
		strings.Join(rows, ","), extra, more, cursor)
}

func newTestReader(t *testing.T, f *fixture) (*Reader, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	r, err := New(Options{
		BaseURL:        srv.URL,
		HTTPClient:     srv.Client(),
		Identity:       &Identity{DeviceID: "1234567890123456789", InstallID: "9876543210987654321"},
		PageGap:        0,
		MaxCues:        100,
		MaxPages:       10,
		PageSize:       20,
		RequestTimeout: 5 * time.Second,
		Now:            func() time.Time { return time.Unix(1793755860, 0) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, srv
}

// 三页数据：验证分页、清洗、去重、排序、过滤与请求体字段。
func TestFetchPaginatesCleansAndSorts(t *testing.T) {
	f := &fixture{pages: map[int64]string{
		0: page(30000, true,
			fixtureRow(testVideo, "第三条", 1, 8000),
			fixtureRow(testVideo, "第一条", 1, 1000),
			fixtureRow(testVideo, "待审核", 0, 2000),                    // status != 1 丢弃
			fixtureRow("9999999999999999999", "别的视频", 1, 3000),       // group_id 不符丢弃
			fixtureRow(testVideo, "第三条", 1, 8000),                    // 重复丢弃
			fixtureRow(testVideo, "换\n行", 1, 9000),                   // 控制字符换成空格
			fixtureRow(testVideo, strings.Repeat("长", 201), 1, 9500), // 超长丢弃
		),
		30000: page(60000, true,
			fixtureRow(testVideo, "第二条", 1, 1500),
			fixtureRow(testVideo, "越界", 1, 8*24*60*60*1000), // 超过 7 天丢弃
		),
		60000: page(0, false), // 空页 + has_more=false 结束
	}}
	r, _ := newTestReader(t, f)

	cues, err := r.Fetch(context.Background(), testSeries, testVideo)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := []Cue{{Time: 1, Text: "第一条"}, {Time: 1.5, Text: "第二条"}, {Time: 8, Text: "第三条"}, {Time: 9, Text: "换 行"}}
	if len(cues) != len(want) {
		t.Fatalf("cues = %+v, want %+v", cues, want)
	}
	for i := range want {
		if cues[i] != want[i] {
			t.Fatalf("cues[%d] = %+v, want %+v", i, cues[i], want[i])
		}
	}
	if !f.hasHdr {
		t.Fatal("签名头没有发出")
	}
	if len(f.bodies) != 3 {
		t.Fatalf("请求次数 = %d, want 3", len(f.bodies))
	}
	b := f.bodies[1]
	if b["group_id"] != testVideo || b["comment_source"] != float64(601) || b["group_type"] != float64(30) {
		t.Fatalf("请求体字段不对: %v", b)
	}
	param := b["business_param"].(map[string]any)
	if param["book_id"] != testSeries || param["start_offset_time"] != float64(30000) {
		t.Fatalf("business_param 不对: %v", param)
	}
	if f.starts[0] != 0 || f.starts[1] != 30000 || f.starts[2] != 60000 {
		t.Fatalf("分页窗口 = %v", f.starts)
	}
}

// 取消必须立刻生效，且不返回半截数据。
func TestFetchCancelStops(t *testing.T) {
	f := &fixture{pages: map[int64]string{0: page(30000, true)}, block: make(chan struct{})}
	r, _ := newTestReader(t, f)
	defer close(f.block) // 释放被阻塞的 handler，避免 httptest.Server.Close 等它
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.Fetch(ctx, testSeries, testVideo)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Fetch 没有响应取消")
	}
}

// 上游一直说「还有更多」时，必须在 MaxCues 处停住。
func TestFetchBoundedByMaxCues(t *testing.T) {
	pages := map[int64]string{}
	for p := int64(0); p < 12; p++ {
		rows := make([]string, 0, 10)
		for i := int64(0); i < 10; i++ {
			rows = append(rows, fixtureRow(testVideo, fmt.Sprintf("第%d页第%d条", p, i), 1, int((p*10+i)*100)))
		}
		pages[p*30000] = page(int((p+1)*30000), true, rows...)
	}
	f := &fixture{pages: pages}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	r, err := New(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), Identity: &Identity{DeviceID: "1234567890123456789", InstallID: "9876543210987654321"}, PageSize: 20, MaxCues: 25, MaxPages: 50, Now: func() time.Time { return time.Unix(1793755860, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	cues, err := r.Fetch(context.Background(), testSeries, testVideo)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(cues) != 25 {
		t.Fatalf("len = %d, want 25", len(cues))
	}
	for i := 1; i < len(cues); i++ {
		if cues[i].Time < cues[i-1].Time {
			t.Fatalf("结果未按时间升序: %v then %v", cues[i-1].Time, cues[i].Time)
		}
	}
	// 3 页正好凑到 25 条，第 4 页不应再请求。
	if len(f.bodies) != 3 {
		t.Fatalf("请求次数 = %d, want 3", len(f.bodies))
	}
}

// 分页上限必须生效：上游永远说有更多时也要停下。
func TestFetchBoundedByMaxPages(t *testing.T) {
	pages := map[int64]string{}
	for p := int64(0); p < 20; p++ {
		pages[p*30000] = page(int((p+1)*30000), true, fixtureRow(testVideo, fmt.Sprintf("第%d页", p), 1, int(p*1000)))
	}
	f := &fixture{pages: pages}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	r, err := New(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), Identity: &Identity{DeviceID: "1234567890123456789", InstallID: "9876543210987654321"}, MaxCues: 1000, MaxPages: 3, Now: func() time.Time { return time.Unix(1793755860, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	cues, err := r.Fetch(context.Background(), testSeries, testVideo)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(cues) != 3 || len(f.bodies) != 3 {
		t.Fatalf("cues = %d, requests = %d, want 3/3", len(cues), len(f.bodies))
	}
}

// 上游返回非 0 code 时，错误必须可分类，且不能带出 URL / 设备 / 签名。
func TestFetchRejectedRedacts(t *testing.T) {
	f := &fixture{pages: map[int64]string{0: ""}, code: "110001"}
	r, _ := newTestReader(t, f)
	_, err := r.Fetch(context.Background(), testSeries, testVideo)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	for _, bad := range []string{"http://", "https://", "_rticket", "device_id", "X-Gorgon", "1234567890123456789"} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("错误信息泄露了 %q: %v", bad, err)
		}
	}
}

func TestFetchInvalidIDs(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range [][2]string{{"", testVideo}, {testSeries, "v02ebeg"}, {testSeries, "123"}} {
		if _, err := r.Fetch(context.Background(), tc[0], tc[1]); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("Fetch(%q,%q) err = %v, want ErrInvalidOptions", tc[0], tc[1], err)
		}
	}
}

// 首页成功、次页失败时，保留已读到的部分并标记 ErrPartial。
func TestFetchPartialOnLaterFailure(t *testing.T) {
	f := &fixture{pages: map[int64]string{
		0: page(30000, true, fixtureRow(testVideo, "第一条", 1, 1000)),
	}}
	r, _ := newTestReader(t, f)
	cues, err := r.Fetch(context.Background(), testSeries, testVideo)
	if !errors.Is(err, ErrPartial) {
		t.Fatalf("err = %v, want ErrPartial", err)
	}
	if len(cues) != 1 || cues[0].Text != "第一条" {
		t.Fatalf("cues = %+v", cues)
	}
}

// 空列表 + 无更多，才算「本集没有弹幕」。
func TestFetchEmptyIsNotAnError(t *testing.T) {
	f := &fixture{pages: map[int64]string{0: page(0, false)}}
	r, _ := newTestReader(t, f)
	cues, err := r.Fetch(context.Background(), testSeries, testVideo)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(cues) != 0 {
		t.Fatalf("cues = %+v", cues)
	}
}

func TestNewRejectsNegativeLimits(t *testing.T) {
	if _, err := New(Options{MaxCues: -1}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("err = %v", err)
	}
	if _, err := New(Options{PageGap: -time.Second}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("err = %v", err)
	}
}

// 默认配置必须给出官方主机与有界上限，防止零值 Options 变成无界读取。
func TestDefaultsAreBounded(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.base != DefaultBaseURL || r.maxCues != defaultMaxCues || r.maxPages != defaultMaxPages || r.pageGap <= 0 || r.client.Timeout <= 0 {
		t.Fatalf("defaults = %+v", r)
	}
}
