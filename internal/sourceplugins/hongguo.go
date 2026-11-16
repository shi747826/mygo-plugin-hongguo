package sourceplugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/shi747826/mygo-plugin-hongguo/internal/account"
	"github.com/shi747826/mygo-plugin-hongguo/internal/danmaku"
	catalog "github.com/shi747826/mygo-plugin-hongguo/internal/hongguocatalog"
	"github.com/shi747826/mygo-plugin-hongguo/internal/playback"
	"github.com/shi747826/mygo-plugin-hongguo/pluginapi"
	"sync"
)

type hongguoCatalog interface {
	Discover(context.Context, string, string) (catalog.Page, error)
	Search(context.Context, string, string) (catalog.Page, error)
	Rank(context.Context) (catalog.Page, error)
	Detail(context.Context, string) (catalog.Series, []catalog.Episode, error)
	Seasons(context.Context, string) ([]catalog.Series, error)
	Covers(context.Context, []string) (map[string]string, error)
	Calendar(context.Context, int) (map[string]catalog.UpcomingEntry, error)
	Playback(context.Context, string) (*model.Playback, error)
	ResetDevice(context.Context) error
}
type cueReader interface {
	Fetch(context.Context, string, string) ([]danmaku.Cue, error)
}

// Hongguo owns all source-specific state, including prepared media and account
// credentials. Close must be called after Serve returns.
type Hongguo struct {
	catalog  hongguoCatalog
	player   *playback.Manager
	cues     cueReader
	mu       sync.Mutex
	sessions map[string]*playback.Session
	order    []string
	closed   bool
	account  *hongguoAccount
}

func NewHongguo(dataDir, ffmpeg string) (*Hongguo, error) {
	c, err := catalog.New(dataDir)
	if err != nil {
		return nil, err
	}
	dev, err := account.LoadOrCreateDevice(dataDir)
	if err != nil {
		return nil, err
	}
	reader, err := danmaku.New(danmaku.Options{Identity: &danmaku.Identity{DeviceID: dev.DeviceID, InstallID: dev.InstallID, CDID: dev.CDID, OpenUDID: dev.OpenUDID}})
	if err != nil {
		return nil, err
	}
	acct, err := newHongguoAccount(dataDir)
	if err != nil {
		return nil, err
	}
	player, err := playback.New(ffmpeg)
	if err != nil {
		return nil, err
	}
	return &Hongguo{catalog: c, player: player, cues: reader, sessions: map[string]*playback.Session{}, account: acct}, nil
}

func (h *Hongguo) Handle(ctx context.Context, method string, raw json.RawMessage) (value any, err error) {
	defer func() { err = classify(err) }()
	p, err := params(raw)
	if err != nil {
		return nil, err
	}
	switch method {
	case "Ping":
		return 2, nil
	case "Discover":
		return h.catalog.Discover(ctx, p.Channel, p.Cursor)
	case "Search":
		return h.catalog.Search(ctx, p.Search.Query, p.Search.Cursor)
	case "Rank":
		return h.catalog.Rank(ctx)
	case "Detail":
		s, e, err := h.catalog.Detail(ctx, p.ID)
		return pluginapi.DetailResult{Series: s, Episodes: e}, err
	case "Seasons":
		return h.catalog.Seasons(ctx, p.ID)
	case "Covers":
		return h.catalog.Covers(ctx, p.IDs)
	case "Calendar":
		return h.catalog.Calendar(ctx, p.Days)
	case "ResetDevice":
		return nil, h.catalog.ResetDevice(ctx)
	case "Resolve":
		return h.resolve(ctx, p.ID, p.Quality)
	case "Release":
		return nil, h.release(p.SessionID)
	case "Danmaku":
		cues, err := h.cues.Fetch(ctx, p.SeriesID, p.ID)
		out := make([]pluginapi.Cue, 0, len(cues))
		for _, v := range cues {
			out = append(out, pluginapi.Cue{Time: v.Time, Text: v.Text})
		}
		return out, err
	default:
		if h.account != nil {
			return h.account.handle(ctx, method, raw)
		}
		return nil, pluginapi.ErrUnsupported
	}
}

func (h *Hongguo) resolve(ctx context.Context, id, quality string) (pluginapi.Media, error) {
	pb, err := h.catalog.Playback(ctx, id)
	if errors.Is(err, sdk.ErrAnonymousLimited) || errors.Is(err, sdk.ErrDeviceRejected) {
		if e := h.catalog.ResetDevice(ctx); e != nil {
			return pluginapi.Media{}, e
		}
		pb, err = h.catalog.Playback(ctx, id)
	}
	// Protected playback deliberately returns both a model and ErrDRMProtected.
	if err != nil && !errors.Is(err, sdk.ErrDRMProtected) {
		return pluginapi.Media{}, err
	}
	if pb == nil {
		return pluginapi.Media{}, pluginapi.ErrContentUnavailable
	}
	s, err := h.player.Prepare(ctx, pb, quality)
	if err != nil {
		return pluginapi.Media{}, err
	}
	token := make([]byte, 24)
	if _, err = rand.Read(token); err != nil {
		_ = s.Close()
		return pluginapi.Media{}, fmt.Errorf("cannot allocate media session")
	}
	sid := hex.EncodeToString(token)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		_ = s.Close()
		return pluginapi.Media{}, pluginapi.ErrClosed
	}
	if h.sessions == nil {
		h.sessions = map[string]*playback.Session{}
	}
	// A crashed or forgetful host cannot accumulate unlimited temporary files.
	if len(h.order) >= 16 {
		old := h.order[0]
		h.order = h.order[1:]
		_ = h.sessions[old].Close()
		delete(h.sessions, old)
	}
	h.sessions[sid] = s
	h.order = append(h.order, sid)
	qualities := []string{}
	seen := map[string]bool{}
	for _, v := range pb.Streams {
		if v.Definition != "" && !seen[v.Definition] {
			seen[v.Definition] = true
			qualities = append(qualities, v.Definition)
		}
	}
	return pluginapi.Media{URL: s.URL, Definition: s.Definition, Qualities: qualities, SessionID: sid}, nil
}
func (h *Hongguo) release(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[id]; s != nil {
		delete(h.sessions, id)
		for i, v := range h.order {
			if v == id {
				h.order = append(h.order[:i], h.order[i+1:]...)
				break
			}
		}
		return s.Close()
	}
	return nil
}
func (h *Hongguo) Close() error {
	h.mu.Lock()
	h.closed = true
	h.sessions = nil
	h.order = nil
	h.mu.Unlock()
	if h.player != nil {
		return h.player.Close()
	}
	return nil
}
