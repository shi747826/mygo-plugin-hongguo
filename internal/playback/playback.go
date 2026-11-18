// Package playback prepares complete MP4 files for native video playback.
package playback

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/shi747826/mygo-plugin-hongguo/internal/cenc"
)

const (
	maxConcurrent        = 2
	maxFileBytes   int64 = 2 << 30
	prepareTimeout       = 15 * time.Minute
	networkTimeout       = 30 * time.Second
)

var ErrClosed = errors.New("playback manager is closed")

// DisableFFmpeg selects direct playback only, without resolving or launching an executable.
// Mobile callers use this even when an FFmpeg executable exists on the device.
const DisableFFmpeg = "mobile:no-ffmpeg"

var ErrUnsupported = errors.New("this stream requires FFmpeg; protected or separate audio streams are unsupported in mobile playback")

// Manager owns a loopback HTTP server and all of its prepared files.
// Close cancels active preparations and releases all sessions.
type Manager struct {
	mu         sync.Mutex
	closed     bool
	closeDone  chan struct{}
	closeErr   error
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	slots      chan struct{}
	sessions   map[string]*Session
	server     *http.Server
	origin     string
	ffmpeg     string
	directOnly bool
	// Limits are private so tests can exercise failure paths cheaply.
	maxBytes int64
	timeout  time.Duration
}

// Session describes the selected representation. Close is safe to call repeatedly.
// Codec is preserved: remuxing does not make HEVC decodable on unsupported systems.
type Session struct {
	URL        string
	Definition string
	Codec      string
	manager    *Manager
	token      string
	mu         sync.Mutex
	closed     bool
	file       string
	dir        string
}

// New starts an HTTP server on an ephemeral IPv4 loopback port. ffmpeg is
// resolved from the argument, MYGO_PLAYER_FFMPEG, then PATH; it is never downloaded.
// Direct playback remains available when ffmpeg is not installed.
func New(ffmpegPath string) (*Manager, error) {
	directOnly := ffmpegPath == DisableFFmpeg
	if directOnly {
		ffmpegPath = ""
	}
	if !directOnly && ffmpegPath == "" {
		ffmpegPath = os.Getenv("MYGO_PLAYER_FFMPEG")
	}
	if !directOnly && ffmpegPath == "" {
		ffmpegPath, _ = exec.LookPath("ffmpeg")
	} else if !directOnly {
		resolved, err := exec.LookPath(ffmpegPath)
		if err != nil {
			return nil, errors.New("configured ffmpeg executable is unavailable")
		}
		ffmpegPath = resolved
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start playback server: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{ctx: ctx, cancel: cancel, closeDone: make(chan struct{}), slots: make(chan struct{}, maxConcurrent), sessions: make(map[string]*Session), origin: "http://" + ln.Addr().String(), ffmpeg: ffmpegPath, directOnly: directOnly, maxBytes: maxFileBytes, timeout: prepareTimeout}
	m.server = &http.Server{Handler: withAppCORS(http.HandlerFunc(m.serve)), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() { _ = m.server.Serve(ln) }()
	return m, nil
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

// Prepare selects quality and either returns a validated original URL or prepares
// a complete seekable MP4. Errors deliberately omit media addresses and secrets.
func (m *Manager) Prepare(ctx context.Context, pb *model.Playback, quality string) (*Session, error) {
	if ctx == nil {
		return nil, errors.New("playback context is missing")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stream, err := pb.Select(quality)
	if err != nil {
		return nil, errors.New("requested playback quality is unavailable")
	}
	if !validURL(stream.URL) || (pb.AudioURL != "" && !validURL(pb.AudioURL)) {
		return nil, errors.New("media URL must use HTTP or HTTPS without credentials")
	}
	s := &Session{Definition: stream.Definition, Codec: stream.Codec, manager: m}
	if !pb.Protected && !stream.Protected && pb.AudioURL == "" {
		s.URL = stream.URL
		return m.register(ctx, s)
	}
	if m.directOnly {
		// Mobile hosts cannot run FFmpeg. A single CENC encrypted stream is
		// decrypted in Go and served from the loopback server; anything else
		// (a separate audio track, subsample encryption) stays unsupported.
		if pb.AudioURL != "" {
			return nil, ErrUnsupported
		}
		if !pb.Protected && !stream.Protected {
			s.URL = stream.URL
			return m.register(ctx, s)
		}
		key, err := contentKey(pb)
		if err != nil {
			// Without a derivable key there is nothing a mobile host can play.
			return nil, ErrUnsupported
		}
		dir, err := os.MkdirTemp("", "hongguo-mobile-")
		if err != nil {
			return nil, errors.New("unable to create playback temporary directory")
		}
		s.dir, s.file = dir, filepath.Join(dir, "video.mp4")
		success := false
		defer func() {
			if !success {
				_ = s.Close()
			}
		}()
		data, err := download(ctx, stream.URL, m.maxBytes)
		if err != nil {
			return nil, err
		}
		plain, err := cenc.Decrypt(data, key)
		if err != nil {
			if errors.Is(err, cenc.ErrUnsupported) {
				return nil, ErrUnsupported
			}
			return nil, errors.New("unable to decrypt this stream")
		}
		if err := os.WriteFile(s.file, plain, 0o600); err != nil {
			return nil, errors.New("unable to store the decrypted stream")
		}
		success = true
		return m.register(ctx, s)
	}
	if m.ffmpeg == "" {
		return nil, errors.New("ffmpeg is required to prepare this stream; set MYGO_PLAYER_FFMPEG or install ffmpeg")
	}
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var key string
	if pb.Protected || stream.Protected {
		// ContentKey requires the model-level flag even when only the stream is marked.
		copyPB := *pb
		copyPB.Protected = true
		k, err := sdk.ContentKey(&copyPB)
		if err != nil {
			return nil, errors.New("unable to derive playback content key")
		}
		key = k.Hex
	}
	dir, err := os.MkdirTemp("", "hongguo-playback-")
	if err != nil {
		return nil, errors.New("unable to create playback temporary directory")
	}
	s.dir, s.file = dir, filepath.Join(dir, "video.mp4")
	success := false
	defer func() {
		if !success {
			_ = s.Close()
		}
	}()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	input := func(raw string) {
		args = append(args, "-rw_timeout", fmt.Sprint(networkTimeout.Microseconds()), "-protocol_whitelist", "http,https,httpproxy,tcp,tls,crypto")
		if key != "" {
			args = append(args, "-decryption_key", key)
		}
		args = append(args, "-i", raw)
	}
	input(stream.URL)
	if pb.AudioURL != "" {
		input(pb.AudioURL)
		args = append(args, "-map", "0:v:0", "-map", "1:a:0")
	} else {
		args = append(args, "-map", "0:v:0?", "-map", "0:a:0?")
	}
	args = append(args, "-c", "copy")
	// AVFoundation expects the hvc1 sample entry for HEVC in MP4. The service
	// uses bytevc1 for HEVC; preserve Codec metadata for native capability errors.
	switch strings.ToLower(strings.TrimSpace(stream.Codec)) {
	case "bytevc1", "hevc", "h265", "h.265", "hvc1", "hev1":
		args = append(args, "-tag:v", "hvc1")
	}
	args = append(args, "-movflags", "+faststart", "-fs", fmt.Sprint(m.maxBytes), s.file)
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	cmd := exec.CommandContext(runCtx, m.ffmpeg, args...)
	// Keep a bounded private diagnostic buffer. Only fixed classifications,
	// never stderr text or substrings, may cross the Prepare error boundary.
	stderr := &diagnosticBuffer{}
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, errors.New("unable to start ffmpeg")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	tooLarge := false
	var runErr error
waitLoop:
	for {
		select {
		case runErr = <-done:
			break waitLoop
		case <-ticker.C:
			if info, err := os.Stat(s.file); err == nil && info.Size() >= m.maxBytes {
				tooLarge = true
				runCancel()
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, statErr := os.Stat(s.file)
	if tooLarge || (statErr == nil && info.Size() >= m.maxBytes) {
		return nil, errors.New("prepared media exceeds disk size limit")
	}
	if runErr != nil {
		return nil, errors.New("ffmpeg could not remux media: " + classifyDiagnostic(stderr.data))
	}
	if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, errors.New("ffmpeg produced no playable media")
	}
	registered, err := m.register(ctx, s)
	if err != nil {
		return nil, err
	}
	success = true
	return registered, nil
}

const diagnosticLimit = 32 << 10

// diagnosticBuffer continues draining the process after reaching its memory
// limit. Returning the original length avoids breaking ffmpeg's stderr pipe.
// It has one writer (os/exec's copy goroutine), and is read only after Wait.
type diagnosticBuffer struct{ data []byte }

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := diagnosticLimit - len(b.data); remaining > 0 {
		b.data = append(b.data, p[:min(len(p), remaining)]...)
	}
	return n, nil
}

var httpDiagnosticStatus = regexp.MustCompile(`(?:http error |server returned |http/[0-9.]+ )([45][0-9]{2})\b`)

// classifyDiagnostic returns an allowlisted label, never a redacted version of
// raw text. This also protects secrets split across lines or truncated at limit.
func classifyDiagnostic(raw []byte) string {
	text := strings.ToLower(string(raw))
	contains := func(patterns ...string) bool {
		for _, pattern := range patterns {
			if strings.Contains(text, pattern) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("not on whitelist", "protocol_whitelist", "protocol not found", "protocol is not supported"):
		return "protocol whitelist rejected an input protocol"
	case httpDiagnosticStatus.MatchString(text):
		// The regular expression permits only a three-digit 4xx/5xx status.
		return "HTTP status " + httpDiagnosticStatus.FindStringSubmatch(text)[1]
	case contains("decrypt", "encryption key", "invalid encryption", "cenc"):
		return "decryption failed or encryption format is unsupported"
	case contains("codec", "muxer", "could not find tag", "could not write header", "not currently supported in container"):
		return "codec or MP4 mux compatibility failure"
	default:
		return "media preparation failed (unclassified)"
	}
}

func (m *Manager) register(ctx context.Context, s *Session) (*Session, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, errors.New("unable to create playback URL token")
	}
	s.token = hex.EncodeToString(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.file != "" {
		s.URL = m.origin + "/media/" + s.token
	}
	m.sessions[s.token] = s
	return s, nil
}

func (m *Manager) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Host != strings.TrimPrefix(m.origin, "http://") {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/media/")
	m.mu.Lock()
	s := m.sessions[token]
	m.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, "/media/") || s == nil {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	if s.closed || s.file == "" {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(s.file)
	s.mu.Unlock()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "video.mp4", info.ModTime(), f)
}

func (s *Session) Close() error {
	if s.manager != nil {
		s.manager.mu.Lock()
		delete(s.manager.sessions, s.token)
		s.manager.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.dir != "" {
		return os.RemoveAll(s.dir)
	}
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		<-m.closeDone
		return m.closeErr
	}
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	serverErr := m.server.Close()
	m.wg.Wait()
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	var errs []error
	if serverErr != nil {
		errs = append(errs, serverErr)
	}
	for _, s := range sessions {
		if err := s.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	m.closeErr = errors.Join(errs...)
	close(m.closeDone)
	return m.closeErr
}

// contentKey derives the AES key that decrypts a protected stream.
func contentKey(pb *model.Playback) ([]byte, error) {
	copyPB := *pb
	copyPB.Protected = true
	derived, err := sdk.ContentKey(&copyPB)
	if err != nil {
		return nil, errors.New("unable to derive playback content key")
	}
	key, err := hex.DecodeString(derived.Hex)
	if err != nil || len(key) != 16 {
		return nil, errors.New("unable to derive playback content key")
	}
	return key, nil
}

// download reads a whole stream into memory, bounded by limit, so it can be
// decrypted in place before it is stored and served.
func download(ctx context.Context, raw string, limit int64) ([]byte, error) {
	if !validURL(raw) {
		return nil, ErrUnsupported
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, ErrUnsupported
	}
	resp, err := (&http.Client{Timeout: networkTimeout}).Do(req)
	if err != nil {
		return nil, errors.New("unable to download the stream")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errors.New("the media server rejected the request")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errors.New("unable to download the stream")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("the stream is too large to prepare on this device")
	}
	return data, nil
}
