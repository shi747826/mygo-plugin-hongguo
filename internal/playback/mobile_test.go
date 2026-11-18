package playback

import (
	"context"
	"errors"
	"github.com/beilunyang/openhongguo/sdk/model"
	"testing"
)

func TestMobileDirectOnly(t *testing.T) {
	// Even an invalid configured desktop FFmpeg must not be resolved in mobile mode.
	t.Setenv("MYGO_PLAYER_FFMPEG", "/invalid/ffmpeg")
	m, err := New(DisableFFmpeg)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	direct := &model.Playback{Streams: []model.Stream{{URL: "https://invalid.example/direct.m3u8", Definition: "720p"}}}
	s, err := m.Prepare(context.Background(), direct, "720p")
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != direct.Streams[0].URL {
		t.Fatal("direct URL changed")
	}
	_ = s.Close()
	for _, pb := range []*model.Playback{
		{Protected: true, Streams: direct.Streams},
		{Streams: []model.Stream{{URL: "https://invalid.example/protected", Protected: true}}},
		{AudioURL: "https://invalid.example/audio", Streams: direct.Streams},
	} {
		if _, err := m.Prepare(context.Background(), pb, ""); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("FFmpeg-dependent stream: %v", err)
		}
	}
}
