package playback

import (
	"strings"
	"testing"
)

func TestDiagnosticsExposeOnlyFixedReason(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"Protocol 'httpproxy' not on whitelist at https://private.example/?token=SECRET", "protocol whitelist rejected an input protocol"},
		{"https://private.example/?key=SECRET: Server returned 403 Forbidden", "HTTP status 403"},
		{"decryption failed for SECRET", "decryption failed or encryption format is unsupported"},
		{"Could not write header: SECRET", "codec or MP4 mux compatibility failure"},
		{"SECRET cannot be read", "media preparation failed (unclassified)"},
	} {
		got := classifyDiagnostic([]byte(tc.raw))
		if got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
		if strings.Contains(got, "SECRET") || strings.Contains(got, "private.example") {
			t.Fatalf("sensitive diagnostics escaped: %q", got)
		}
	}
}
func TestDiagnosticsBoundedButKeepDraining(t *testing.T) {
	b := &diagnosticBuffer{}
	for i := 0; i < 100; i++ {
		p := []byte(strings.Repeat("secret", 1000))
		n, e := b.Write(p)
		if e != nil || n != len(p) {
			t.Fatal(n, e)
		}
	}
	if len(b.data) != diagnosticLimit {
		t.Fatal(len(b.data))
	}
}
