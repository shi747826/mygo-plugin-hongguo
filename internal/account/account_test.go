package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestClient 把客户端的官方域名指向测试服务器。
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New()
	// 让固定域名解析到测试服务器，同时保留同源校验语义。
	c.http.Transport = &rewriteTransport{base: srv.URL, inner: http.DefaultTransport}
	return c
}

type rewriteTransport struct {
	base  string
	inner http.RoundTripper
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "hongguoduanju.com" {
		parsed, err := url.Parse(rt.base)
		if err != nil {
			return nil, err
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme = parsed.Scheme
		clone.URL.Host = parsed.Host
		clone.Host = parsed.Host
		return rt.inner.RoundTrip(clone)
	}
	return rt.inner.RoundTrip(req)
}

func qrBody(status int) string {
	encoded := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'})
	_ = status
	return `{"message":"success","data":{"token":"tok-123","qrcode":"` + encoded + `","expire_time":0}}`
}

// loginFlowServer 是一个完整的有状态扫码服务：pending -> scanned -> confirmed，
// 确认后通过官方域 302 跳转下发 Cookie，最后资料接口校验会话。
func loginFlowServer(t *testing.T) *httptest.Server {
	t.Helper()
	var polls int
	mux := http.NewServeMux()
	mux.HandleFunc("/passport/web/get_qrcode/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(qrBody(0)))
	})
	mux.HandleFunc("/passport/web/check_qrconnect/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "tok-123" {
			t.Errorf("token = %q", r.URL.Query().Get("token"))
		}
		polls++
		switch polls {
		case 1:
			_, _ = w.Write([]byte(`{"message":"success","data":{"status":1}}`))
		case 2:
			_, _ = w.Write([]byte(`{"message":"success","data":{"status":"2"}}`))
		default:
			_, _ = w.Write([]byte(`{"message":"success","data":{"status":3,"redirect_url":"https://hongguoduanju.com/passport/web/callback/"}}`))
		}
	})
	mux.HandleFunc("/passport/web/callback/", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "sess-abc", Path: "/", Secure: true, HttpOnly: true})
		_, _ = w.Write([]byte(`{"message":"success","data":{}}`))
	})
	mux.HandleFunc("/passport/account/info/v2/", func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie("sessionid")
		if err != nil || ck.Value != "sess-abc" {
			_, _ = w.Write([]byte(`{"message":"error","error_code":13,"data":{"error_code":13,"description":"会话过期"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":"success","data":{"error_code":0,"user_id":42,"nickname":"阿红"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginPendingConfirmedAndProfile(t *testing.T) {
	srv := loginFlowServer(t)
	c := New()
	c.http.Transport = &rewriteTransport{base: srv.URL, inner: http.DefaultTransport}
	ctx := context.Background()

	ch, err := c.StartLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Token != "tok-123" || len(ch.Image) == 0 {
		t.Fatalf("challenge = %+v", ch)
	}

	statuses := []LoginStatus{LoginPending, LoginPending, LoginConfirmed}
	for i, want := range statuses {
		got, err := c.PollLogin(ctx, ch.Token)
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("poll %d = %q want %q", i, got, want)
		}
	}
	if !strings.Contains(c.CookieHeader(), "sessionid=sess-abc") {
		t.Fatalf("cookie not captured: %q", c.CookieHeader())
	}
	p, err := c.Profile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "42" || p.Nickname != "阿红" {
		t.Fatalf("profile = %+v", p)
	}
}

func TestPollLoginExpiredNumericAndString(t *testing.T) {
	for _, body := range []string{
		`{"message":"success","data":{"status":5}}`,
		`{"message":"success","data":{"status":"expired"}}`,
		`{"message":"success","data":{"status":"5"}}`,
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		got, err := c.PollLogin(context.Background(), "tok")
		if err != nil || got != LoginExpired {
			t.Fatalf("%s: got %q err=%v", body, got, err)
		}
	}
}

func TestPollLoginUnknownStatusStaysPending(t *testing.T) {
	// 线上未扫码时实测返回 "new"；其他未知状态也不能打断轮询，
	// 否则服务端新增状态值会让扫码流程直接失败。
	for _, body := range []string{
		`{"message":"success","data":{"status":"new"}}`,
		`{"message":"success","data":{"status":"open"}}`,
		`{"message":"success","data":{"status":99}}`,
		`{"message":"success","data":{"status":{"x":1}}}`,
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		got, err := c.PollLogin(context.Background(), "tok")
		if err != nil || got != LoginPending {
			t.Fatalf("%s: got %q err=%v, want pending", body, got, err)
		}
	}
}

func TestPollLoginMalformedIsError(t *testing.T) {
	// 只有整体不是 JSON 才算错误；单个未知/异常的状态字段按等待处理。
	for _, body := range []string{`not-json`} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		if got, err := c.PollLogin(context.Background(), "tok"); !errors.Is(err, ErrUpstream) {
			t.Fatalf("%s: got %q err=%v, want ErrUpstream", body, got, err)
		}
	}
}

func TestPollLoginConfirmedRejectsForeignRedirect(t *testing.T) {
	foreign := []string{
		"https://evil.example/callback",
		"http://hongguoduanju.com/passport/web/callback/",
		"https://hongguoduanju.com:8443/callback",
		"https://user:pass@hongguoduanju.com/callback",
	}
	for _, redirect := range foreign {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"message":"success","data":{"status":3,"redirect_url":"` + redirect + `"}}`))
		})
		if got, err := c.PollLogin(context.Background(), "tok"); !errors.Is(err, ErrUpstream) || got == LoginConfirmed {
			t.Fatalf("%s: got %q err=%v", redirect, got, err)
		}
	}
}

func TestPollLoginConfirmedFollowsRelativeOfficialRedirect(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/passport/web/check_qrconnect/":
			_, _ = w.Write([]byte(`{"message":"success","data":{"status":3,"redirect_url":"/passport/web/callback/"}}`))
		case "/passport/web/callback/":
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "s", Path: "/", Secure: true, HttpOnly: true})
			_, _ = w.Write([]byte(`{"message":"success","data":{}}`))
		case "/passport/account/info/v2/":
			if _, err := r.Cookie("sessionid"); err != nil {
				_, _ = w.Write([]byte(`{"message":"error","error_code":13,"data":{}}`))
				return
			}
			_, _ = w.Write([]byte(`{"message":"success","data":{"user_id":7,"nickname":"n"}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	got, err := c.PollLogin(context.Background(), "tok")
	if err != nil || got != LoginConfirmed {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestResolveOfficialRejectsUnsafeForms(t *testing.T) {
	for _, raw := range []string{
		"//evil.example/x",
		"https://evil.example/x",
		"http://hongguoduanju.com/x",
		"https://user:pass@hongguoduanju.com/x",
		"javascript:alert(1)",
	} {
		if got, err := resolveOfficial(raw); err == nil {
			t.Fatalf("%q accepted as %q", raw, got)
		}
	}
	for raw, want := range map[string]string{
		"/passport/web/callback/":             "https://hongguoduanju.com/passport/web/callback/",
		"https://www.hongguoduanju.com/x?y=1": "https://www.hongguoduanju.com/x?y=1",
	} {
		got, err := resolveOfficial(raw)
		if err != nil || got != want {
			t.Fatalf("%q: got %q err=%v", raw, got, err)
		}
	}
}

func TestPollLoginConfirmedCallbackRejectsCrossDomain(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/passport/web/check_qrconnect/" {
			_, _ = w.Write([]byte(`{"message":"success","data":{"status":3,"redirect_url":"https://hongguoduanju.com/passport/web/callback/"}}`))
			return
		}
		http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
	})
	if got, err := c.PollLogin(context.Background(), "tok"); !errors.Is(err, ErrUpstream) || got == LoginConfirmed {
		t.Fatalf("got %q err=%v, want rejection", got, err)
	}
}

func TestPollLoginConfirmedDoesNotReplayRedirect(t *testing.T) {
	var callbacks int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/passport/web/check_qrconnect/":
			_, _ = w.Write([]byte(`{"message":"success","data":{"status":3,"redirect_url":"https://hongguoduanju.com/passport/web/callback/"}}`))
		case "/passport/web/callback/":
			callbacks++
			http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "s", Path: "/", Secure: true, HttpOnly: true})
			_, _ = w.Write([]byte(`{"message":"success","data":{}}`))
		case "/passport/account/info/v2/":
			if _, err := r.Cookie("sessionid"); err != nil {
				_, _ = w.Write([]byte(`{"message":"error","error_code":13,"data":{}}`))
				return
			}
			_, _ = w.Write([]byte(`{"message":"success","data":{"user_id":7,"nickname":"n"}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	for i := 0; i < 3; i++ {
		if got, err := c.PollLogin(context.Background(), "tok"); err != nil || got != LoginConfirmed {
			t.Fatalf("poll %d: got %q err=%v", i, got, err)
		}
	}
	if callbacks != 1 {
		t.Fatalf("redirect followed %d times, want exactly 1", callbacks)
	}
}

func TestPollLoginConfirmedWithExistingSessionSkipsRedirect(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/passport/web/check_qrconnect/":
			_, _ = w.Write([]byte(`{"message":"success","data":{"status":3,"redirect_url":"https://hongguoduanju.com/passport/web/callback/"}}`))
		case "/passport/account/info/v2/":
			if _, err := r.Cookie("sessionid"); err != nil {
				_, _ = w.Write([]byte(`{"message":"error","error_code":13,"data":{}}`))
				return
			}
			_, _ = w.Write([]byte(`{"message":"success","data":{"user_id":7,"nickname":"n"}}`))
		default:
			t.Errorf("redirect should not be followed, got %q", r.URL.Path)
		}
	})
	c.SetCookieHeader("sessionid=s")
	if got, err := c.PollLogin(context.Background(), "tok"); err != nil || got != LoginConfirmed {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPollLoginConfirmedWithoutSessionIsNotConfirmed(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/passport/account/info/v2/" {
			_, _ = w.Write([]byte(`{"message":"success","data":{"error_code":0}}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":"success","data":{"status":3}}`))
	})
	if got, err := c.PollLogin(context.Background(), "tok"); got == LoginConfirmed || err == nil {
		t.Fatalf("got %q err=%v, want failure without verified profile", got, err)
	}
}

func TestPollLoginEmptyTokenExpires(t *testing.T) {
	c := New()
	if got, err := c.PollLogin(context.Background(), "  "); err != nil || got != LoginExpired {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPollLoginHonorsCancellation(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.PollLogin(ctx, "tok"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestSameHostPolicy(t *testing.T) {
	allowed := []string{
		"https://hongguoduanju.com/passport/web/check/",
		"https://www.hongguoduanju.com/x",
		"https://hongguoduanju.com:443/x",
	}
	for _, raw := range allowed {
		u, _ := url.Parse(raw)
		if !sameHost(u) {
			t.Errorf("sameHost(%q) = false", raw)
		}
	}
	blocked := []string{
		"http://hongguoduanju.com/x",
		"https://evil.example/x",
		"https://hongguoduanju.com.evil.example/x",
		"https://evil-hongguoduanju.com/x",
		"https://user:pass@hongguoduanju.com/x",
		"https://hongguoduanju.com:8443/x",
		"https://hongguoduanju.com@evil.example/x",
	}
	for _, raw := range blocked {
		u, _ := url.Parse(raw)
		if sameHost(u) {
			t.Errorf("sameHost(%q) = true", raw)
		}
	}
}

func TestSafeRedirectStripsCredentials(t *testing.T) {
	target, _ := url.Parse("https://www.hongguoduanju.com/passport/web/callback/")
	req, _ := http.NewRequest(http.MethodGet, target.String(), nil)
	req.Header.Set("Cookie", "sessionid=secret")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Proxy-Authorization", "Basic secret")
	req.Header.Set("User-Agent", userAgent)
	if err := safeRedirect(req, nil); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
		if v := req.Header.Get(h); v != "" {
			t.Fatalf("%s leaked across redirect: %q", h, v)
		}
	}
	if req.Header.Get("User-Agent") != userAgent {
		t.Fatal("user agent dropped")
	}
}

func TestSafeRedirectRejectsForeignAndInsecure(t *testing.T) {
	for _, target := range []string{
		"https://evil.example/callback",
		"http://hongguoduanju.com/callback",
		"https://user:pass@hongguoduanju.com/callback",
		"https://hongguoduanju.com:8443/callback",
	} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if err := safeRedirect(req, nil); !errors.Is(err, ErrUpstream) {
			t.Fatalf("%s: err = %v, want ErrUpstream", target, err)
		}
	}
	official, _ := url.Parse("https://hongguoduanju.com/x")
	req, _ := http.NewRequest(http.MethodGet, official.String(), nil)
	via := make([]*http.Request, 5)
	if err := safeRedirect(req, via); !errors.Is(err, ErrUpstream) {
		t.Fatalf("redirect loop err = %v", err)
	}
}

func TestStartLoginRejectsCrossDomainRedirect(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
	})
	if _, err := c.StartLogin(context.Background()); !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
}

func TestStartLoginRejectsInsecureRedirect(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://hongguoduanju.com/passport/web/plain", http.StatusFound)
	})
	if _, err := c.StartLogin(context.Background()); !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
}

func TestStartLoginRejectsErrorEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"error","error_code":9,"data":{"description":"token=leaked-secret"}}`))
	})
	_, err := c.StartLogin(context.Background())
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	if strings.Contains(err.Error(), "leaked-secret") {
		t.Fatalf("upstream description leaked: %v", err)
	}
}

func TestStartLoginRejectsOversizedBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxBody+1))
	})
	if _, err := c.StartLogin(context.Background()); !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
}

func TestStartLoginRejectsBadQRCode(t *testing.T) {
	for _, data := range []string{
		`{"token":"t","qrcode":""}`,
		`{"token":"","qrcode":"aGk="}`,
		`{"token":"t","qrcode":"%%%not-base64%%%"}`,
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"message":"success","data":` + data + `}`))
		})
		if _, err := c.StartLogin(context.Background()); !errors.Is(err, ErrUpstream) {
			t.Fatalf("%s: err = %v", data, err)
		}
	}
}

func TestProfileSessionExpired(t *testing.T) {
	for _, body := range []string{
		`{"message":"error","data":{"error_code":13,"description":"会话过期，请重新登录"}}`,
		`{"message":"error","error_code":13,"data":{}}`,
		`{"message":"error","data":{"description":"会话过期"}}`,
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		if _, err := c.Profile(context.Background()); !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("%s: err = %v, want ErrSessionExpired", body, err)
		}
	}
}

func TestProfileParsesAccount(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"success","data":{"error_code":0,"user_id_str":"42","nickname":"阿红","avatar_url":"https://x.invalid/a.png"}}`))
	})
	p, err := c.Profile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "42" || p.Nickname != "阿红" || p.Display() != "阿红" {
		t.Fatalf("profile = %+v", p)
	}
	// 数字型 user_id 也必须读出来。
	c = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"success","data":{"error_code":0,"user_id":42,"nickname":"阿红"}}`))
	})
	if p, err = c.Profile(context.Background()); err != nil || p.UserID != "42" {
		t.Fatalf("numeric user id: %+v err=%v", p, err)
	}
}

func TestProfileRejectsEmptyOrErrorResult(t *testing.T) {
	for _, body := range []string{
		`{"message":"success","data":{"error_code":0}}`,
		`{"message":"success","data":{"error_code":0,"user_id":0,"nickname":""}}`,
		`{"message":"error","data":{"error_code":7,"description":"denied"}}`,
		`garbage`,
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		if p, err := c.Profile(context.Background()); err == nil {
			t.Fatalf("%s: profile accepted: %+v", body, p)
		}
	}
}

func TestSessionPersistenceIsPrivate(t *testing.T) {
	dir := t.TempDir()
	s := Session{Cookie: "sessionid=abc", Profile: Profile{UserID: "1", Nickname: "n"}}
	if err := SaveSession(dir, s); err != nil {
		t.Fatal(err)
	}
	path := SessionPath(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
	got, ok, err := LoadSession(dir)
	if err != nil || !ok || got.Cookie != s.Cookie {
		t.Fatalf("loaded = %+v ok=%v err=%v", got, ok, err)
	}
	if err := ClearSession(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := LoadSession(dir); ok {
		t.Fatal("session still present after clear")
	}
}

func TestSessionRejectsEmptyIdentity(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []Session{
		{},
		{Cookie: "sessionid=abc"},
		{Profile: Profile{UserID: "1"}},
		{Cookie: "   ", Profile: Profile{Nickname: "n"}},
	} {
		if err := SaveSession(dir, s); err == nil {
			t.Fatalf("saved invalid session: %+v", s)
		}
	}
	if _, ok, _ := LoadSession(dir); ok {
		t.Fatal("invalid session loaded")
	}
}

func TestSessionTightensLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	path := SessionPath(dir)
	if err := os.WriteFile(path, []byte(`{"cookie":"sessionid=abc","profile":{"user_id":"1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadSession(dir); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("perm = %o, still world-readable", perm)
	}
}

func TestSessionRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	if err := os.WriteFile(target, []byte(`{"cookie":"sessionid=abc","profile":{"user_id":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := SessionPath(dir)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, _, err := LoadSession(dir); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestSessionRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(SessionPath(dir), make([]byte, maxSessionBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSession(dir); err == nil {
		t.Fatal("oversized session accepted")
	}
}

func TestRedactHidesSecrets(t *testing.T) {
	err := errors.New("GET https://hongguoduanju.com/passport?token=deadbeef&sessionid=secret failed")
	got := redact(err)
	for _, secret := range []string{"hongguoduanju.com", "deadbeef", "sessionid=secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted text leaks %q: %s", secret, got)
		}
	}
}

func TestRejectsNonPassportPath(t *testing.T) {
	c := New()
	if _, err := c.getJSON(context.Background(), "/reading/user/list/", nil); !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
}

func TestClientCookiesRoundTrip(t *testing.T) {
	c := New()
	c.SetCookieHeader("sessionid=abc; sid_tt=def")
	header := c.CookieHeader()
	if !strings.Contains(header, "sessionid=abc") || !strings.Contains(header, "sid_tt=def") {
		t.Fatalf("cookie header = %q", header)
	}
	c.ClearCookies()
	if got := c.CookieHeader(); got != "" {
		t.Fatalf("cookies not cleared: %q", got)
	}
	// 非法 Cookie 不应被接受或导致崩溃。
	c.SetCookieHeader("=bad; ; sessionid; ok=1")
	if got := c.CookieHeader(); !strings.Contains(got, "ok=1") || strings.Contains(got, "=bad") {
		t.Fatalf("cookie header = %q", got)
	}
}

func TestFavoritesParsesShelfItems(t *testing.T) {
	c := New()
	c.appBase = "http://app.invalid"
	// 直接验证解析层：服务端字段名在不同接口里不一致。
	items, err := parseShelf([]byte(`{"video_shelf_info":[
		{"series_title":"甲","series_cover_url":"c1","video_data":{"series_id":"11","episode_cnt":30}},
		{"series_title":"乙","item_id":"12"}
	]}`))
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].SeriesID != "11" || items[0].Title != "甲" || items[0].Episodes != 30 {
		t.Fatalf("first=%+v", items[0])
	}
	if items[1].SeriesID != "12" {
		t.Fatalf("second=%+v", items[1])
	}
}

func TestFavoritesParsesSubscribeItems(t *testing.T) {
	items, err := parseShelf([]byte(`{"subscribe_items":[
		{"item_id":"21","is_online":false,"schedule_publish_time":"2026-11-20","subscribe_data":{"series_id":"21","title":"预告剧","cover":"cv","episode_cnt":0}}
	]}`))
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].SeriesID != "21" || items[0].Title != "预告剧" || items[0].Online || items[0].PublishAt != "2026-11-20" {
		t.Fatalf("item=%+v", items[0])
	}
}

func TestFavoritesEmptyAndNullData(t *testing.T) {
	for _, raw := range []string{"null", "{}"} {
		items, err := parseShelf([]byte(raw))
		if err != nil || len(items) != 0 {
			t.Fatalf("%s: items=%+v err=%v", raw, items, err)
		}
	}
}

func TestEnvelopeMapsAuthCodes(t *testing.T) {
	var env appEnvelope
	if err := json.Unmarshal([]byte(`{"code":101119,"message":"BOOKSHELF_GET_ERROR"}`), &env); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(env.check("收藏列表"), ErrNeedsLogin) {
		t.Fatalf("err=%v", env.check("收藏列表"))
	}
	if err := json.Unmarshal([]byte(`{"code":0,"message":"SUCCESS"}`), &env); err != nil {
		t.Fatal(err)
	}
	if err := env.check("收藏列表"); err != nil {
		t.Fatalf("err=%v", err)
	}
}

func TestReservationsEmptyBodyNeedsLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 匿名身份下预约接口返回 200 空响应。
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New()
	c.appBase = srv.URL
	if _, err := c.Reservations(context.Background(), NewDevice(), true); !errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("err=%v, want ErrNeedsLogin", err)
	}
}

func TestAppDeviceIsDigits(t *testing.T) {
	d := NewDevice()
	for _, v := range []string{d.DeviceID, d.InstallID} {
		if len(v) != 19 || strings.HasPrefix(v, "0") {
			t.Fatalf("bad device id %q", v)
		}
		for _, r := range v {
			if r < '0' || r > '9' {
				t.Fatalf("non digit in %q", v)
			}
		}
	}
}

func TestLoadOrCreateDevicePersists(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateDevice(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.DeviceID) != 19 || len(first.InstallID) != 19 || first.CDID == "" || first.OpenUDID == "" {
		t.Fatalf("bad device %+v", first)
	}
	second, err := LoadOrCreateDevice(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("device changed across loads: %+v vs %+v", first, second)
	}
	info, err := os.Stat(devicePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o", perm)
	}
}

func TestLoadOrCreateDeviceRefusesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(devicePath(dir), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateDevice(dir); err == nil {
		t.Fatal("corrupt device file accepted")
	}
	// 不覆盖原文件，避免把用户标识弄丢。
	data, err := os.ReadFile(devicePath(dir))
	if err != nil || string(data) != "{broken" {
		t.Fatalf("file was overwritten: %q %v", data, err)
	}
}
