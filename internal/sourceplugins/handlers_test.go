package sourceplugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/shi747826/mygo-plugin-hongguo/internal/danmaku"
	catalog "github.com/shi747826/mygo-plugin-hongguo/internal/hongguocatalog"
	"github.com/shi747826/mygo-plugin-hongguo/internal/playback"
	"github.com/shi747826/mygo-plugin-hongguo/pluginapi"
	"strings"
	"testing"
)

type fakeCatalog struct {
	calls, resets int
	errors        []error
	pb            *model.Playback
}

func (f *fakeCatalog) Discover(context.Context, string, string) (catalog.Page, error) {
	return catalog.Page{}, nil
}
func (f *fakeCatalog) Search(context.Context, string, string) (catalog.Page, error) {
	return catalog.Page{}, nil
}
func (f *fakeCatalog) Rank(context.Context) (catalog.Page, error) { return catalog.Page{}, nil }
func (f *fakeCatalog) Detail(context.Context, string) (catalog.Series, []catalog.Episode, error) {
	return catalog.Series{ID: "series"}, []catalog.Episode{{ID: "ep", Number: 1}}, nil
}
func (f *fakeCatalog) Seasons(context.Context, string) ([]catalog.Series, error)   { return nil, nil }
func (f *fakeCatalog) Covers(context.Context, []string) (map[string]string, error) { return nil, nil }
func (f *fakeCatalog) Calendar(context.Context, int) (map[string]catalog.UpcomingEntry, error) {
	return nil, nil
}
func (f *fakeCatalog) ResetDevice(context.Context) error { f.resets++; return nil }
func (f *fakeCatalog) Playback(context.Context, string) (*model.Playback, error) {
	i := f.calls
	f.calls++
	if i < len(f.errors) {
		return f.pb, f.errors[i]
	}
	return f.pb, nil
}

type fakeCues struct{}

func (fakeCues) Fetch(context.Context, string, string) ([]danmaku.Cue, error) {
	return []danmaku.Cue{{Time: 1.5, Text: "hello"}}, nil
}
func newTestHongguo(t *testing.T, f *fakeCatalog) *Hongguo {
	t.Helper()
	p, err := playback.New("")
	if err != nil {
		t.Fatal(err)
	}
	h := &Hongguo{catalog: f, player: p, cues: fakeCues{}}
	t.Cleanup(func() { _ = h.Close() })
	return h
}
func TestHongguoResolveRecoveryAndRelease(t *testing.T) {
	f := &fakeCatalog{errors: []error{sdk.ErrAnonymousLimited}, pb: &model.Playback{Streams: []model.Stream{{URL: "https://example.invalid/720.mp4", Definition: "720p"}, {URL: "https://example.invalid/1080.mp4", Definition: "1080p"}}}}
	h := newTestHongguo(t, f)
	v, err := h.Handle(context.Background(), "Resolve", json.RawMessage(`{"ID":"ep","Quality":"720p"}`))
	if err != nil {
		t.Fatal(err)
	}
	media := v.(pluginapi.Media)
	if media.URL != "https://example.invalid/720.mp4" || media.SessionID == "" || len(media.Qualities) != 2 {
		t.Fatalf("bad media: %+v", media)
	}
	if f.calls != 2 || f.resets != 1 {
		t.Fatalf("calls=%d resets=%d", f.calls, f.resets)
	}
	wire, _ := json.Marshal(media)
	if strings.Contains(string(wire), "Protected") || strings.Contains(string(wire), "KeyMaterial") {
		t.Fatalf("private model escaped: %s", wire)
	}
	if _, err = h.Handle(context.Background(), "Release", json.RawMessage(fmt.Sprintf(`{"SessionID":%q}`, media.SessionID))); err != nil {
		t.Fatal(err)
	}
	if len(h.sessions) != 0 || len(h.order) != 0 {
		t.Fatal("release leaked session")
	}
	if err = h.release(media.SessionID); err != nil {
		t.Fatal("release must be idempotent", err)
	}
}
func TestHongguoRecoveryIsBounded(t *testing.T) {
	f := &fakeCatalog{errors: []error{sdk.ErrAnonymousLimited, sdk.ErrAnonymousLimited, sdk.ErrAnonymousLimited}}
	h := newTestHongguo(t, f)
	_, err := h.Handle(context.Background(), "Resolve", json.RawMessage(`{"ID":"ep"}`))
	if !errors.Is(err, pluginapi.ErrAnonymousLimited) || f.resets != 1 || f.calls != 2 {
		t.Fatalf("err=%v calls=%d resets=%d", err, f.calls, f.resets)
	}
}
func TestHongguoBoundedSessionsAndShutdown(t *testing.T) {
	f := &fakeCatalog{pb: &model.Playback{Streams: []model.Stream{{URL: "https://example.invalid/video.mp4", Definition: "720p"}}}}
	h := newTestHongguo(t, f)
	for i := 0; i < 20; i++ {
		if _, err := h.resolve(context.Background(), "ep", "720p"); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.sessions) != 16 || len(h.order) != 16 {
		t.Fatal("unbounded sessions")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := h.resolve(context.Background(), "ep", "720p")
	if !errors.Is(err, playback.ErrClosed) {
		t.Fatalf("resolve after close: %v", err)
	}
}
func TestHongguoDanmakuAndInvalidRequests(t *testing.T) {
	h := newTestHongguo(t, &fakeCatalog{})
	v, err := h.Handle(context.Background(), "Danmaku", json.RawMessage(`{"SeriesID":"series","ID":"ep"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if string(raw) != `[{"time":1.5,"text":"hello"}]` {
		t.Fatalf("bad cues: %s", raw)
	}
	_, err = h.Handle(context.Background(), "Resolve", json.RawMessage(`{"ID":12}`))
	if !errors.Is(err, pluginapi.ErrInvalidOptions) {
		t.Fatal(err)
	}
	_, err = h.Handle(context.Background(), "Playback", nil)
	if !errors.Is(err, pluginapi.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestAccountLoadAndLogoutAreOffline(t *testing.T) {
	a, err := newHongguoAccount(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.handle(context.Background(), "AccountLoginWait", json.RawMessage(`{"Token":"unknown"}`))
	if fmt.Sprint(v) != "expired" || err != nil {
		t.Fatalf("%v %v", v, err)
	}
	if _, err = a.handle(context.Background(), "AccountLogout", nil); err != nil {
		t.Fatal(err)
	}
}
