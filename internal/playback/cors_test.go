package playback

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithAppCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("media"))
	})
	handler := withAppCORS(next)

	preflight := httptest.NewRequest(http.MethodOptions, "/media/x", nil)
	preflight.Header.Set("Origin", appOrigin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, preflight)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != appOrigin {
		t.Fatalf("allow origin = %q, want %q", got, appOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("preflight is missing allowed methods")
	}

	get := httptest.NewRequest(http.MethodGet, "/media/x", nil)
	get.Header.Set("Origin", appOrigin)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	if rec.Code != http.StatusOK || rec.Body.String() != "media" {
		t.Fatalf("get = %d %q, want 200 media", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	if got := h.Get("Access-Control-Allow-Origin"); got != appOrigin {
		t.Fatalf("allow origin = %q, want %q", got, appOrigin)
	}
	if got := h.Get("Access-Control-Expose-Headers"); got == "" {
		t.Fatal("media response is missing exposed headers")
	}
	if got := h.Get("Vary"); got != "Origin" {
		t.Fatalf("vary = %q, want Origin", got)
	}

	foreign := httptest.NewRequest(http.MethodGet, "/media/x", nil)
	foreign.Header.Set("Origin", "https://example.test")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, foreign)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin received a grant: %q", got)
	}
}
