package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type contract interface {
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
	Close() error
}

var _ contract = (*Source)(nil)

func TestNewInfoAndClose(t *testing.T) {
	s, err := New(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := s.Call(context.Background(), "Info", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifest, &want); err != nil {
		t.Fatal(err)
	}
	delete(got, "runtime")
	delete(want, "runtime")
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatal("Info lost original manifest fields")
	}
	var info struct{ Runtime struct{ Type, API string } }
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	if info.Runtime.Type != "inprocess" || info.Runtime.API != "mobile-v1" {
		t.Fatalf("runtime: %+v", info.Runtime)
	}
	ping, err := s.Call(context.Background(), "Ping", nil)
	if err != nil || string(ping) != "2" {
		t.Fatalf("Ping: %s %v", ping, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call(context.Background(), "Info", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("after Close: %v", err)
	}
}

func TestCallForwardsAllMethodsAndJSON(t *testing.T) {
	methods := []string{"Discover", "Search", "Rank", "Detail", "Resolve", "Release", "Covers", "Seasons", "Calendar", "Danmaku", "ResetDevice", "AccountStatus", "FutureMethod"}
	raw := json.RawMessage(`{"id":"42","search":{"query":"test"}}`)
	var called string
	s := newSource(context.Background(), nil, func(ctx context.Context, method string, p json.RawMessage) (any, error) {
		called = method
		if string(p) != string(raw) {
			t.Fatal("params changed")
		}
		return map[string]any{"original": true}, nil
	}, func() error { return nil })
	defer s.Close()
	for _, method := range methods {
		got, err := s.Call(context.Background(), method, raw)
		if err != nil || called != method || string(got) != `{"original":true}` {
			t.Fatalf("%s: %s %v", method, got, err)
		}
	}
}

func TestCancellationAndCloseWait(t *testing.T) {
	for _, mode := range []string{"request", "parent", "close"} {
		t.Run(mode, func(t *testing.T) {
			parent, parentCancel := context.WithCancel(context.Background())
			defer parentCancel()
			req, reqCancel := context.WithCancel(context.Background())
			defer reqCancel()
			entered := make(chan struct{})
			var closes atomic.Int32
			s := newSource(parent, nil, func(ctx context.Context, method string, p json.RawMessage) (any, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			}, func() error { closes.Add(1); return nil })
			done := make(chan error, 1)
			go func() { _, err := s.Call(req, "Search", nil); done <- err }()
			<-entered
			switch mode {
			case "request":
				reqCancel()
			case "parent":
				parentCancel()
			case "close":
				go s.Close()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("call did not cancel")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if closes.Load() != 1 {
				t.Fatalf("backend closed %d times", closes.Load())
			}
		})
	}
}

func TestManifestInjectionAndCanceledNew(t *testing.T) {
	raw := json.RawMessage(`{"id":"fixture","future":{"keep":true},"runtime":{"type":"exec"}}`)
	got, err := mobileManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(got, &m)
	if string(m["future"]) != `{"keep":true}` {
		t.Fatal("unknown manifest field lost")
	}
	if _, err := mobileManifest([]byte(`null`)); err == nil {
		t.Fatal("accepted invalid manifest")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("New canceled: %v", err)
	}
}
