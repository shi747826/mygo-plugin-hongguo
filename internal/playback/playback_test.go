package playback

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/sdk/model"
)

const fixtureKey = "nLwSxG6JI85zhT//c7AK4HauO+lzrg7TdZwO0XWrP+J0pDypqQ=="

// The test executable doubles as ffmpeg, keeping all tests fully offline.
func TestFFmpegHelper(t *testing.T) {
	if os.Getenv("PLAYBACK_FAKE_FFMPEG") != "1" {
		return
	}
	args := os.Args
	var mediaArgs []string
	for i, a := range args {
		if a == "--" {
			mediaArgs = args[i+1:]
			break
		}
	}
	if len(mediaArgs) == 0 {
		os.Exit(2)
	}
	_ = os.WriteFile(os.Getenv("PLAYBACK_FAKE_ARGS"), []byte(strings.Join(mediaArgs, "\n")), 0600)
	out := mediaArgs[len(mediaArgs)-1]
	switch os.Getenv("PLAYBACK_FAKE_MODE") {
	case "block":
		_ = os.WriteFile(out, []byte("partial"), 0600)
		_ = os.WriteFile(os.Getenv("PLAYBACK_FAKE_STARTED"), []byte(out), 0600)
		for {
			time.Sleep(time.Second)
		}
	case "large":
		_ = os.WriteFile(out, make([]byte, 2048), 0600)
	case "fail":
		_ = os.WriteFile(out, []byte("partial"), 0600)
		_, _ = os.Stderr.WriteString("SECRET https://invalid.example/private?token=SECRET\n")
		os.Exit(1)
	default:
		_ = os.WriteFile(out, []byte("0123456789abcdef"), 0600)
	}
	os.Exit(0)
}

func fakeManager(t *testing.T, mode string) *Manager {
	t.Helper()
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "ffmpeg")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec "+quote(binary)+" -test.run=^TestFFmpegHelper$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLAYBACK_FAKE_FFMPEG", "1")
	t.Setenv("PLAYBACK_FAKE_MODE", mode)
	t.Setenv("PLAYBACK_FAKE_ARGS", filepath.Join(dir, "args"))
	t.Setenv("PLAYBACK_FAKE_STARTED", filepath.Join(dir, "started"))
	m, err := New(script)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m
}

func protected() *model.Playback {
	return &model.Playback{Protected: true, KeyMaterial: fixtureKey, Streams: []model.Stream{{URL: "https://invalid.example/video?secret=private", Definition: "1080p", Codec: "h264"}}}
}

func TestDirectSelectionAndValidation(t *testing.T) {
	m := fakeManager(t, "")
	pb := &model.Playback{Streams: []model.Stream{{URL: "http://localhost:1234/local", Definition: "720p"}, {URL: "https://invalid.example/hi", Definition: "1080p"}}}
	s, err := m.Prepare(context.Background(), pb, "720p")
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != pb.Streams[0].URL || s.Definition != "720p" {
		t.Fatalf("unexpected direct session: %+v", s)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"file:///private/secret", "ftp://invalid.example/media", "http://user:secret@invalid.example/media", "http:///media"} {
		pb.Streams = []model.Stream{{URL: raw}}
		if _, err := m.Prepare(context.Background(), pb, ""); err == nil || strings.Contains(err.Error(), raw) {
			t.Fatalf("invalid URL was accepted or exposed")
		}
	}
	if _, err := m.Prepare(context.Background(), nil, ""); err == nil {
		t.Fatal("nil playback accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Prepare(ctx, protected(), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %v", err)
	}
}

func TestPreparedHTTPRangeTokenAndCleanup(t *testing.T) {
	m := fakeManager(t, "")
	s, err := m.Prepare(context.Background(), protected(), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := s.dir
	if len(s.token) != 64 || s.Codec != "h264" {
		t.Fatal("missing token or codec")
	}
	client := &http.Client{Timeout: time.Second}
	req, _ := http.NewRequest(http.MethodGet, s.URL, nil)
	req.Header.Set("Range", "bytes=3-7")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(data) != "34567" || resp.Header.Get("Content-Range") != "bytes 3-7/16" {
		t.Fatalf("range failed: %d %q", resp.StatusCode, data)
	}
	for _, path := range []string{"/", "/media/", "/media/guessed", "/media/" + s.token + "/video.mp4"} {
		r, err := client.Get(m.origin + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatalf("path %q exposed media: %d", path, r.StatusCode)
		}
	}
	req, _ = http.NewRequest(http.MethodHead, s.URL, nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ContentLength != 16 {
		t.Fatalf("HEAD length: %d", resp.ContentLength)
	}
	req, _ = http.NewRequest(http.MethodGet, s.URL, nil)
	req.Host = "other.example"
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("foreign Host accepted")
	}
	args, err := os.ReadFile(os.Getenv("PLAYBACK_FAKE_ARGS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"-decryption_key\n", "-c\ncopy\n", "-movflags\n+faststart\n", "-rw_timeout\n30000000\n"} {
		if !strings.Contains(string(args), expected) {
			t.Errorf("missing argument %q", expected)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("session directory remains: %v", err)
	}
	resp, err = client.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("closed session still served")
	}
}

func TestSeparateAudioAndHEVCRemux(t *testing.T) {
	m := fakeManager(t, "")
	pb := &model.Playback{AudioURL: "http://localhost:1234/audio", Streams: []model.Stream{{URL: "https://invalid.example/video", Codec: "hevc"}}}
	s, err := m.Prepare(context.Background(), pb, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Codec != "hevc" || !strings.HasPrefix(s.URL, m.origin) {
		t.Fatal("HEVC metadata or local URL lost")
	}
	args, _ := os.ReadFile(os.Getenv("PLAYBACK_FAKE_ARGS"))
	if !strings.Contains(string(args), "-map\n0:v:0\n-map\n1:a:0\n") || strings.Contains(string(args), "-decryption_key") {
		t.Fatal("incorrect separate audio mapping")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Fatal("manager did not remove session")
	}
	if _, err := m.Prepare(context.Background(), pb, ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("Prepare after Close: %v", err)
	}
}

func awaitStarted(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(os.Getenv("PLAYBACK_FAKE_STARTED"))
		if err == nil {
			return string(b)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fake ffmpeg did not start")
	return ""
}

func TestCancelAndCloseDuringPrepare(t *testing.T) {
	for _, closeManager := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "close"}[closeManager], func(t *testing.T) {
			m := fakeManager(t, "block")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := m.Prepare(ctx, protected(), ""); done <- err }()
			out := awaitStarted(t)
			if closeManager {
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Prepare did not stop")
			}
			if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
				t.Fatal("canceled preparation left temporary directory")
			}
		})
	}
}

func TestFailureSizeAndTimeoutLimits(t *testing.T) {
	for _, mode := range []string{"fail", "large", "block"} {
		t.Run(mode, func(t *testing.T) {
			m := fakeManager(t, mode)
			m.maxBytes = 1024
			if mode == "block" {
				m.timeout = time.Second
			}
			s, err := m.Prepare(context.Background(), protected(), "")
			if err == nil || s != nil {
				t.Fatal("expected failure")
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "https://") || strings.Contains(err.Error(), fixtureKey) {
				t.Fatal("sensitive error")
			}
			if mode == "block" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout: %v", err)
			}
			args, readErr := os.ReadFile(os.Getenv("PLAYBACK_FAKE_ARGS"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			parts := strings.Split(string(args), "\n")
			if _, statErr := os.Stat(filepath.Dir(parts[len(parts)-1])); !os.IsNotExist(statErr) {
				t.Fatal("failed preparation left files")
			}
		})
	}
}

func TestHEVCContainerTag(t *testing.T) {
	for _, codec := range []string{"bytevc1", "hevc", "h265", "hvc1", "hev1", "h264"} {
		t.Run(codec, func(t *testing.T) {
			m := fakeManager(t, "")
			pb := protected()
			pb.Streams[0].Codec = codec
			s, err := m.Prepare(context.Background(), pb, "")
			if err != nil {
				t.Fatal(err)
			}
			if s.Codec != codec {
				t.Fatal("original codec metadata lost")
			}
			args, err := os.ReadFile(os.Getenv("PLAYBACK_FAKE_ARGS"))
			if err != nil {
				t.Fatal(err)
			}
			hasTag := strings.Contains(string(args), "-tag:v\nhvc1\n")
			if hasTag != (codec != "h264") {
				t.Fatalf("incorrect hvc1 container tag for %s", codec)
			}
		})
	}
}

func TestFFmpegDiscoveryAndMissingKey(t *testing.T) {
	configured := fakeManager(t, "")
	t.Setenv("MYGO_PLAYER_FFMPEG", configured.ffmpeg)
	m, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if m.ffmpeg != configured.ffmpeg {
		t.Fatal("environment ffmpeg ignored")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYGO_PLAYER_FFMPEG", "")
	t.Setenv("PATH", t.TempDir())
	m, err = New("")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Prepare(context.Background(), protected(), ""); err == nil || !strings.Contains(err.Error(), "ffmpeg is required") {
		t.Fatalf("missing ffmpeg: %v", err)
	}
	pb := &model.Playback{Streams: []model.Stream{{URL: "http://localhost:1234/direct"}}}
	if _, err := m.Prepare(context.Background(), pb, ""); err != nil {
		t.Fatalf("direct playback requires ffmpeg: %v", err)
	}
	bad := protected()
	bad.KeyMaterial = "invalid-secret"
	if _, err := configured.Prepare(context.Background(), bad, ""); err == nil || strings.Contains(err.Error(), bad.KeyMaterial) {
		t.Fatal("invalid content key accepted or exposed")
	}
}

func TestConcurrencySlotCancellation(t *testing.T) {
	m := fakeManager(t, "")
	for i := 0; i < maxConcurrent; i++ {
		m.slots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.Prepare(ctx, protected(), ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued cancellation: %v", err)
	}
	if _, err := os.Stat(os.Getenv("PLAYBACK_FAKE_ARGS")); !os.IsNotExist(err) {
		t.Fatal("ffmpeg started beyond concurrency limit")
	}
	for i := 0; i < maxConcurrent; i++ {
		<-m.slots
	}
}
