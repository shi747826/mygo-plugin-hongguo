// Package mobile adapts the existing source handler for in-process Android hosts.
// Models remain private to this module and cross the boundary only as JSON.
package mobile

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/shi747826/mygo-plugin-hongguo/internal/playback"
	"github.com/shi747826/mygo-plugin-hongguo/internal/sourceplugins"
	"github.com/shi747826/mygo-plugin-hongguo/pluginapi"
)

//go:generate go run ./internal/manifestgen ../plugin.json manifest.json

//go:embed manifest.json
var manifest []byte

// ErrUnsupported and ErrClosed retain this plugin's protocol classifications.
var ErrUnsupported = pluginapi.ErrUnsupported
var ErrClosed = pluginapi.ErrClosed

type handler func(context.Context, string, json.RawMessage) (any, error)

// Source owns the handler, active calls, and any long-running media server.
// The context passed to New controls its entire lifetime. Close is idempotent.
type Source struct {
	ctx          context.Context
	cancel       context.CancelFunc
	handle       handler
	closeBackend func() error
	info         json.RawMessage
	mu           sync.Mutex
	closed       bool
	calls        sync.WaitGroup
	done         chan struct{}
	closeErr     error
}

// New creates a source without starting a plugin process. FFmpeg is disabled:
// direct streams work, while protected or separate-audio streams report unsupported.
func New(ctx context.Context, dataDir string) (*Source, error) {
	return NewWithManifest(ctx, dataDir, manifest)
}

// NewWithManifest lets a build script inject the repository's full plugin.json.
// Only runtime is replaced; unknown manifest fields are retained.
func NewWithManifest(ctx context.Context, dataDir string, raw json.RawMessage) (*Source, error) {
	if ctx == nil {
		return nil, fmt.Errorf("source context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("source data directory is required")
	}
	info, err := mobileManifest(raw)
	if err != nil {
		return nil, err
	}
	backend, err := sourceplugins.NewHongguo(dataDir, playback.DisableFFmpeg)
	if err != nil {
		return nil, err
	}
	return newSource(ctx, info, backend.Handle, backend.Close), nil
}

func mobileManifest(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("invalid source manifest")
	}
	fields["runtime"] = json.RawMessage(`{"type":"inprocess","api":"mobile-v1"}`)
	return json.Marshal(fields)
}

func newSource(ctx context.Context, info json.RawMessage, h handler, closeBackend func() error) *Source {
	life, cancel := context.WithCancel(ctx)
	s := &Source{ctx: life, cancel: cancel, info: info, handle: h, closeBackend: closeBackend, done: make(chan struct{})}
	context.AfterFunc(life, func() { _ = s.Close() })
	return s
}

// Call preserves the existing handler's method names and params/result JSON fields.
// Each call inherits both its request context and the source lifetime context.
func (s *Source) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		return nil, fmt.Errorf("request context is required")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	s.calls.Add(1)
	s.mu.Unlock()
	defer s.calls.Done()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	if method == "Info" {
		return append(json.RawMessage(nil), s.info...), nil
	}
	value, err := s.handle(callCtx, method, params)
	if errors.Is(err, playback.ErrUnsupported) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, playback.ErrUnsupported)
	}
	if err != nil {
		return nil, err
	}
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// Close cancels active calls, waits for them, then stops the media server.
func (s *Source) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.done
		return s.closeErr
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.calls.Wait()
	s.closeErr = s.closeBackend()
	close(s.done)
	return s.closeErr
}
