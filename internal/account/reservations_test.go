package account

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPersonalReservationsReadAllPages(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/reading/user/subscribe/list/v1/" {
			t.Errorf("unexpected reservation request")
		}
		q := r.URL.Query()
		for k, v := range map[string]string{"tab_type": "13", "is_online": "false", "limit": "20", "subscribe_offset": "0", "subscribe_order_type": "0", "swipe_type": "0", "need_calendar_schema": "false", "user_is_login": "1", "device_platform": "android", "app_name": "novelread", "aid": "8662"} {
			if q.Get(k) != v {
				t.Errorf("query %s=%q want %q", k, q.Get(k), v)
			}
		}
		for _, k := range []string{"device_id", "iid", "version_name", "device_type", "os_version", "resolution"} {
			if q.Get(k) == "" {
				t.Errorf("missing device field %s", k)
			}
		}
		if r.Header.Get("x-medusa") == "" || r.Header.Get("x-gorgon") == "" {
			t.Error("missing account signatures")
		}
		if cookie, err := r.Cookie("sessionid"); err != nil || cookie.Value != "fixture-session" {
			t.Error("missing fixture login")
		}
		if r.Header.Get("X-SS-DP") != "8662" {
			t.Error("missing app identity header")
		}
		switch q.Get("offset") {
		case "0":
			if q.Has("session_id") {
				t.Error("first page must not send a snapshot session")
			}
			fmt.Fprint(w, `{"code":0,"data":{"subscribe_items":[{"item_id":"80001","name":"预约甲","cover":"https://example.invalid/a.jpg","schedule_publish_time":1794873600,"is_online":false}],"has_more":true,"next_offset":20,"session_id":"fixture-snapshot"}}`)
		case "20":
			if q.Get("session_id") != "fixture-snapshot" {
				t.Error("continuation lost server snapshot")
			}
			fmt.Fprint(w, `{"code":0,"data":{"subscribe_items":[{"item_id":"80001","name":"预约甲","schedule_publish_time":1794873600},{"item_id":80002,"name":"预约乙","schedule_publish_time":0,"expected_publish_time":"1794960000"}],"has_more":false}}`)
		default:
			t.Error("unexpected offset")
			fmt.Fprint(w, `{"code":8}`)
		}
	}))
	defer server.Close()
	c := New()
	c.appBase = server.URL
	c.SetCookieHeader("sessionid=fixture-session")
	items, err := c.Reservations(context.Background(), NewDevice(), false)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(items) != 2 {
		t.Fatalf("calls=%d items=%d", calls, len(items))
	}
	if items[0].Title != "预约甲" || items[0].PublishAt != "1794873600" || items[0].Online {
		t.Fatalf("first item not parsed correctly: %+v", items[0])
	}
	if items[1].SeriesID != "80002" || items[1].PublishAt != "1794960000" {
		t.Fatalf("expected date fallback failed: %+v", items[1])
	}
}

func TestPersonalReservationsOnlineSeriesIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("is_online") != "true" {
			t.Error("wrong release-status query")
		}
		fmt.Fprint(w, `{"code":0,"data":{"subscribe_items":[{"item_id":999,"name":"已播预约","is_online":true,"schedule_publish_time":"1794873600","subscribe_data":{"series_id":1234567,"title":"已播预约","cover":"https://example.invalid/p.jpg","episode_cnt":24}}],"has_more":false}}`)
	}))
	defer server.Close()
	c := New()
	c.appBase = server.URL
	c.SetCookieHeader("sessionid=fixture-session")
	items, err := c.Reservations(context.Background(), NewDevice(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SeriesID != "1234567" || !items[0].Online || items[0].Episodes != 24 {
		t.Fatalf("released episode must use playable series ID: %+v", items)
	}
}

func TestPersonalReservationMalformedResponsesAreNotEmptyLists(t *testing.T) {
	for _, body := range []string{
		"", `not json`, `{}`, `{"code":0}`, `{"code":0,"data":{}}`,
		`{"code":0,"data":{"subscribe_items":null}}`,
		`{"code":0,"data":{"subscribe_items":[{"unexpected":1}]}}`,
		`{"code":0,"data":{"subscribe_items":[],"has_more":true,"next_offset":20}}`,
		`{"code":0,"data":{"subscribe_items":[{"item_id":1,"name":"重复页"}],"has_more":true,"next_offset":0}}`,
	} {
		t.Run(fmt.Sprint(len(body), "-", strings.Count(body, "{")), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			c := New()
			c.appBase = server.URL
			c.SetCookieHeader("sessionid=fixture-session")
			if _, err := c.Reservations(context.Background(), NewDevice(), false); err == nil {
				t.Fatal("invalid response must not report no reservations")
			}
		})
	}
}

func TestPersonalReservationEmptyAndExpiredSessions(t *testing.T) {
	for _, tc := range []struct {
		body    string
		expired bool
	}{
		{`{"code":0,"data":{"subscribe_items":[],"has_more":false}}`, false},
		{`{"code":8,"message":"expired"}`, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
		c := New()
		c.appBase = server.URL
		c.SetCookieHeader("sessionid=fixture-session")
		rows, err := c.Reservations(context.Background(), NewDevice(), false)
		if tc.expired && !errors.Is(err, ErrNeedsLogin) {
			t.Errorf("expired login must be detected: %v", err)
		}
		if !tc.expired && (err != nil || len(rows) != 0) {
			t.Errorf("valid empty personal list: %v", err)
		}
		server.Close()
	}
}
