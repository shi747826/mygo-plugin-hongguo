package account

import (
	"context"
	"os"
	"testing"
	"time"
)

// Explicit opt-in uses the saved local session for read-only verification.
// Diagnostics report counts only, never cookies, account IDs or media URLs.
func TestLivePersonalReservations(t *testing.T) {
	if os.Getenv("HONGGUO_ACCOUNT_RESERVATIONS_LIVE") != "1" {
		t.Skip("set HONGGUO_ACCOUNT_RESERVATIONS_LIVE=1 for read-only account verification")
	}
	dir := os.Getenv("MYGO_PLUGIN_DATA_DIR")
	if dir == "" {
		t.Fatal("set MYGO_PLUGIN_DATA_DIR to this plugin's isolated saved-session directory")
	}
	session, ok, err := LoadSession(dir)
	if err != nil || !ok {
		t.Fatal("saved login unavailable")
	}
	c := New()
	c.SetCookieHeader(session.Cookie)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := c.Profile(ctx); err != nil {
		t.Fatal("saved login verification failed")
	}
	dev, err := LoadReservationDevice(ctx, dir)
	if err != nil {
		t.Fatal("registered device unavailable")
	}
	for _, online := range []bool{false, true} {
		items, err := c.Reservations(ctx, dev, online)
		if err != nil {
			t.Fatalf("online=%v: %v", online, err)
		}
		dates := 0
		for _, item := range items {
			if item.Title == "" || item.SeriesID == "" {
				t.Fatal("missing reservation title or identity")
			}
			if item.PublishAt != "" {
				dates++
			}
		}
		t.Logf("personal online=%v items=%d with_release_dates=%d", online, len(items), dates)
	}
}
