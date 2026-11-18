package playback

import "net/http"

// appOrigin is the page origin the desktop and mobile hosts serve the app
// from. Requests to the loopback media server come from that page, so the
// server answers with the matching CORS grant instead of a wildcard.
const appOrigin = "https://mygo.localhost"

// withAppCORS lets the app page read the loopback stream. The media server
// stays bound to 127.0.0.1; CORS only decides which page may read it.
func withAppCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin == appOrigin {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", appOrigin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Range")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
