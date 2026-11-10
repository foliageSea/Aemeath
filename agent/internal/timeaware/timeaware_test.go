package timeaware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var externalInstant = time.Date(2026, 7, 1, 23, 30, 45, 123456700, time.UTC)
var systemInstant = time.Date(2001, 1, 2, 3, 4, 5, 0, time.UTC)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := New()
	c.httpClient = server.Client()
	c.endpoint = server.URL
	c.local = time.FixedZone("TestLocal", 9*3600)
	c.now = func() time.Time { return systemInstant }
	return c
}

func goodResponse(w http.ResponseWriter, instant time.Time) {
	w.Header().Set("Date", instant.Format(http.TimeFormat))
	fmt.Fprintf(w, `{"dateTime":%q,"timeZone":"UTC"}`, instant.Format("2006-01-02T15:04:05.999999999"))
}

func TestNetworkProjection(t *testing.T) {
	var requests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || !strings.Contains(r.Header.Get("Cache-Control"), "no-cache") || r.Header.Get("Pragma") != "no-cache" {
			t.Error("missing fresh GET request headers")
		}
		goodResponse(w, externalInstant)
	})
	c.now = func() time.Time { t.Error("network result consulted system clock"); return systemInstant }
	cases := []struct {
		zone, datetime, offset, weekday string
		dst                             bool
	}{
		{"Asia/Shanghai", "2026-07-02T07:30:45.1234567+08:00", "+08:00", "星期四", false},
		{"America/New_York", "2026-07-01T19:30:45.1234567-04:00", "-04:00", "星期三", true},
		{"UTC", "2026-07-01T23:30:45.1234567Z", "+00:00", "星期三", false},
		{"", "2026-07-02T08:30:45.1234567+09:00", "+09:00", "星期四", false},
		{"Local", "2026-07-02T08:30:45.1234567+09:00", "+09:00", "星期四", false},
	}
	for _, tc := range cases {
		t.Run(tc.zone, func(t *testing.T) {
			info, err := c.Now(context.Background(), tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			if info.DateTime != tc.datetime || info.UTCOffset != tc.offset || info.Weekday != tc.weekday || info.IsDST != tc.dst {
				t.Fatalf("wrong projection: %+v", info)
			}
			if info.UTC != externalInstant.Format(time.RFC3339Nano) || info.Unix != externalInstant.Unix() || info.Source != "network" || info.Provider != provider || info.Notice != "" {
				t.Fatalf("wrong instant/source: %+v", info)
			}
			expectedZone := tc.zone
			if tc.zone == "" || tc.zone == "Local" {
				expectedZone = "TestLocal"
			}
			if info.Timezone != expectedZone || info.Date != tc.datetime[:10] || info.Time != tc.datetime[11:19] {
				t.Fatalf("wrong presentation: %+v", info)
			}
			b, err := json.Marshal(info)
			if err != nil || strings.Contains(string(b), "notice") {
				t.Fatalf("notice should be omitted: %s, %v", b, err)
			}
		})
	}
	if requests.Load() != int32(len(cases)) {
		t.Fatal("requests were cached")
	}
}

func TestDSTTransition(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	before := formatInfo(time.Date(2026, 3, 8, 6, 59, 59, 0, time.UTC), loc)
	after := formatInfo(time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), loc)
	if before.Time != "01:59:59" || before.UTCOffset != "-05:00" || before.IsDST || after.Time != "03:00:00" || after.UTCOffset != "-04:00" || !after.IsDST || after.Weekday != "星期日" {
		t.Fatalf("bad DST transition: %+v -> %+v", before, after)
	}
}

func TestInvalidTimezoneNeverRequestsOrFallsBack(t *testing.T) {
	c := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid timezone made request") })
	c.now = func() time.Time { t.Error("invalid timezone used fallback"); return systemInstant }
	for _, zone := range []string{"Mars/Olympus", "../UTC", strings.Repeat("a", 129), strings.Repeat("中", 43), " UTC", "UTC\x00"} {
		info, err := c.Now(context.Background(), zone)
		if err == nil || !strings.Contains(err.Error(), "invalid timezone") || info != (Info{}) {
			t.Fatalf("invalid zone accepted: %q, %+v, %v", zone, info, err)
		}
	}
}

func assertFallback(t *testing.T, c *Client) {
	t.Helper()
	info, err := c.Now(context.Background(), "Asia/Shanghai")
	if err != nil {
		t.Fatalf("service failure should fallback: %v", err)
	}
	if info.Source != "system_fallback" || info.Provider != "system" || info.Notice != fallbackNotice || info.UTC != systemInstant.Format(time.RFC3339Nano) || info.DateTime != "2001-01-02T11:04:05+08:00" {
		t.Fatalf("wrong fallback: %+v", info)
	}
	encoded, _ := json.Marshal(info)
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("external response leaked")
	}
}

func TestServiceFailureFallback(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		headers map[string]string
	}{
		{name: "http failure", status: 503, body: "secret"},
		{name: "invalid json", body: "secret"},
		{name: "trailing json", body: `{"dateTime":"2026-07-01T23:30:45","timeZone":"UTC"} {}`},
		{name: "error json", body: `{"error":"secret"}`},
		{name: "wrong types", body: `{"dateTime":123,"timeZone":"UTC"}`},
		{name: "invalid instant", body: `{"dateTime":"secret","timeZone":"UTC"}`},
		{name: "wrong timezone", body: `{"dateTime":"2026-07-01T23:30:45","timeZone":"Europe/Berlin"}`},
		{name: "oversized", body: strings.Repeat("x", maxBodyBytes+1)},
		{name: "oversized valid json", body: `{"dateTime":"2026-07-01T23:30:45","timeZone":"UTC"}` + strings.Repeat(" ", maxBodyBytes)},
		{name: "stale date", headers: map[string]string{"Date": "Wed, 01 Jul 2026 22:30:45 GMT"}},
		{name: "future date", headers: map[string]string{"Date": "Thu, 02 Jul 2026 23:30:45 GMT"}},
		{name: "invalid date", headers: map[string]string{"Date": "secret"}},
		{name: "stale age", headers: map[string]string{"Age": "31"}},
		{name: "negative age", headers: map[string]string{"Age": "-1"}},
		{name: "invalid age", headers: map[string]string{"Age": "secret"}},
		{name: "huge age", headers: map[string]string{"Age": "18446744073709551616"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Date", externalInstant.Format(http.TimeFormat))
				for key, value := range tc.headers {
					w.Header().Set(key, value)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				if tc.body != "" {
					io.WriteString(w, tc.body)
				} else {
					fmt.Fprintf(w, `{"dateTime":%q,"timeZone":"UTC"}`, externalInstant.Format("2006-01-02T15:04:05.999999999"))
				}
			})
			assertFallback(t, c)
		})
	}
}

func TestRedirectRefused(t *testing.T) {
	var followed atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true); goodResponse(w, externalInstant) }))
	defer destination.Close()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) })
	assertFallback(t, c)
	if followed.Load() {
		t.Fatal("followed redirect")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("secret body error") }
func (brokenBody) Close() error             { return nil }

func TestTransportAndReadErrors(t *testing.T) {
	for _, bodyFailure := range []bool{false, true} {
		c := New()
		c.now = func() time.Time { return systemInstant }
		c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if bodyFailure {
				return &http.Response{StatusCode: 200, Body: brokenBody{}, Header: make(http.Header)}, nil
			}
			return nil, errors.New("secret transport error")
		})}
		assertFallback(t, c)
	}
}

func TestInternalTimeoutFallback(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c.timeout = 20 * time.Millisecond
	started := time.Now()
	assertFallback(t, c)
	if time.Since(started) > time.Second {
		t.Fatal("internal timeout was not bounded")
	}
}

func TestParentCancellationNeverFallsBack(t *testing.T) {
	for _, mode := range []string{"before", "during", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() })
			c.now = func() time.Time { t.Error("canceled request used fallback"); return systemInstant }
			ctx, cancel := context.WithCancel(context.Background())
			expected := context.Canceled
			if mode == "before" {
				cancel()
			}
			if mode == "during" {
				go func() { <-entered; cancel() }()
			}
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
				expected = context.DeadlineExceeded
			}
			defer cancel()
			info, err := c.Now(ctx, "UTC")
			if !errors.Is(err, expected) || info != (Info{}) {
				t.Fatalf("cancellation masked: %+v, %v", info, err)
			}
		})
	}
}

func TestConcurrentCalls(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { goodResponse(w, externalInstant) })
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := c.Now(context.Background(), "Asia/Shanghai")
			if err != nil || info.Source != "network" {
				t.Errorf("concurrent call failed: %+v, %v", info, err)
			}
		}()
	}
	wg.Wait()
}

func TestSmoke(t *testing.T) {
	if os.Getenv("TIMEAWARE_SMOKE") != "1" {
		t.Skip("set TIMEAWARE_SMOKE=1 for real provider smoke")
	}
	info, err := New().Now(context.Background(), "Asia/Shanghai")
	if err != nil {
		t.Fatal("smoke failed")
	}
	t.Logf("Source=%s Provider=%s DateTime=%s timezone=%s", info.Source, info.Provider, info.DateTime, info.Timezone)
	if info.Source != "network" {
		t.Fatal("external provider unavailable")
	}
}
