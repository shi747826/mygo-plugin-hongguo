package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shi747826/mygo-plugin-hongguo/internal/playback"
	"strings"
	"testing"
)

func TestFFmpegFailureIsExplicitlyUnsupported(t *testing.T) {
	s := newSource(context.Background(), nil, func(context.Context, string, json.RawMessage) (any, error) {
		return nil, playback.ErrUnsupported
	}, func() error { return nil })
	defer s.Close()
	raw, err := s.Call(context.Background(), "Resolve", json.RawMessage(`{"id":"fixture"}`))
	if len(raw) != 0 || !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "FFmpeg") {
		t.Fatalf("Resolve: %s %v", raw, err)
	}
}
