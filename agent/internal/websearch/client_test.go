package websearch

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
	"testing"
	"time"
	"unicode/utf8"
)

func fixture(items string) string { return `{"results":[` + items + `]}` }
func item(title, link, snippet string) string {
	b, _ := json.Marshal(map[string]string{"title": title, "url": link, "content": snippet})
	return string(b)
}
func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c := New("test-key")
	c.endpoint = s.URL + "/search"
	c.httpClient = s.Client()
	return c
}
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("TAVILY_LIVE_SMOKE") != "1" || strings.TrimSpace(os.Getenv("TAVILY_API_KEY")) == "" {
		t.Skip("opt-in authenticated Tavily request")
	}
	got, err := New(os.Getenv("TAVILY_API_KEY")).Search(context.Background(), "ESP32-S3 official documentation", 3)
	if err != nil {
		t.Fatalf("Tavily: %v (provider/network may restrict access)", err)
	}
	if len(got.Results) == 0 {
		t.Fatal("no usable results")
	}
	t.Logf("%d results; first source: %s", len(got.Results), got.Results[0].URL)
}
func TestSearch(t *testing.T) {
	body := fixture(item("<b>中文 &amp; 标题</b>", "https://example.com/a#one", "<p>中文&nbsp;摘要</p><script>ignore()</script> 更多") + "," + item("duplicate", "https://example.com/a#two", "") + "," + item("bad", "http://127.0.0.1", "") + "," + item("", "https://example.com/blank", "") + "," + item("missing", "", "") + "," + item("second", "https://example.com/b", "") + "," + item("third", "https://example.com/c", "") + "," + item("fourth", "https://example.com/d", "") + "," + item("fifth", "https://example.com/e", ""))
	requestedLimits := make(chan int, 6)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("bad request method, path, or headers")
		}
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "test-key") || strings.Contains(r.URL.String(), "test-key") {
			t.Error("credential outside authorization")
		}
		var fields map[string]any
		if json.Unmarshal(raw, &fields) != nil || len(fields) != 7 || fields["query"] != "中文 搜索" || fields["search_depth"] != "basic" || fields["topic"] != "general" || fields["include_answer"] != false || fields["include_raw_content"] != false || fields["auto_parameters"] != false {
			t.Error("bad request body")
		}
		n, ok := fields["max_results"].(float64)
		if !ok || n < 1 || n > 5 {
			t.Error("bad max_results")
		}
		requestedLimits <- int(n)
		io.WriteString(w, body)
	})
	for limit := 0; limit <= 5; limit++ {
		got, err := c.Search(context.Background(), " 中文\t\n搜索 ", limit)
		want := limit
		if want == 0 {
			want = 3
		}
		if requested := <-requestedLimits; requested != want {
			t.Errorf("max_results = %d, want %d", requested, want)
		}
		if got.Query != "中文 搜索" {
			t.Error("query not preserved")
		}
		if err != nil || len(got.Results) != want {
			t.Fatalf("%+v %v", got, err)
		}
		first := got.Results[0]
		if first.Title != "中文 & 标题" || first.Snippet != "中文 摘要 更多" || first.URL != "https://example.com/a" {
			t.Fatal(first)
		}
	}
}
func TestInput(t *testing.T) {
	for _, q := range []string{"", " \t\n", strings.Repeat("中", 257), strings.Repeat("😀", 257), "secret\x00query", "secret\x1bquery", string([]byte{255})} {
		if _, err := New("test-key").Search(context.Background(), q, 3); err != ErrQuery {
			t.Fatal(err)
		}
	}
	for _, q := range []string{strings.Repeat("中", 256), strings.Repeat("😀", 256)} {
		if _, err := normalizeQuery(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{-1, 6} {
		if _, err := New("test-key").Search(context.Background(), "query", limit); err != ErrLimit {
			t.Fatal(err)
		}
	}
}
func TestURLs(t *testing.T) {
	bad := []string{"http://localhost", "http://a.localhost/", "http://localhost./", "http://127.0.0.1/", "http://127.1/", "http://2130706433/", "http://0x7f000001/", "http://10.0.0.1/", "http://172.16.0.1/", "http://192.168.0.1/", "http://169.254.169.254/", "http://100.64.0.1/", "http://0.0.0.0/", "http://192.0.2.1/", "http://224.0.0.1/", "http://[::1]/", "http://[::ffff:127.0.0.1]/", "http://[fc00::1]/", "http://[fe80::1]/", "http://[2001:db8::1]/", "https://user:secret@example.com/", "javascript:alert(1)", "data:text/plain,x", "/relative", "//example.com", "https://printer.local", "https://home.arpa", "http://intranet", "https://example.com../", "https://example.com:99999/", "https://example.com/a b", "https://example.com/" + strings.Repeat("a", 1024)}
	for _, raw := range bad {
		if ValidPublicURL(raw) {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"https://docs.espressif.com/中文", "http://8.8.8.8/", "https://[2606:4700:4700::1111]/", "https://example.com:443/"} {
		if !ValidPublicURL(raw) {
			t.Errorf("rejected %s", raw)
		}
	}
}
func TestResponseFailures(t *testing.T) {
	for _, body := range []string{"", "<html>challenge</html>", "{", "null", "[]", "{}", `{"results":null}`, `{"results":{}}`, `{"results":[]} {}`, `{"results":[]}garbage`, `{"detail":{"error":"secret"}}`, `{"results":[],"error":"secret"}`, `{"results":[{"title":3}]}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		if _, err := c.Search(context.Background(), "secret", 3); err != ErrInvalidResponse {
			t.Errorf("%q: %v", body, err)
		}
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, fixture("")) })
	got, err := c.Search(context.Background(), "query", 3)
	if err != nil || got.Results == nil || len(got.Results) != 0 {
		t.Fatal(got, err)
	}
}
func TestBounds(t *testing.T) {
	for _, known := range []bool{true, false} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if known {
				w.Header().Set("Content-Length", fmt.Sprint(maxBody+1))
			} else {
				w.(http.Flusher).Flush()
			}
			io.WriteString(w, strings.Repeat("x", maxBody+1))
		})
		if _, err := c.Search(context.Background(), "size", 3); err != ErrTooLarge {
			t.Fatal(err)
		}
	}
	var items strings.Builder
	for i := 0; i < 8; i++ {
		if i > 0 {
			items.WriteString(",")
		}
		items.WriteString(item(strings.Repeat("😀", 150), fmt.Sprintf("https://example.com/%d?", i)+strings.Repeat("&", 950), strings.Repeat("中", 300)))
	}
	got, err := parseResponse([]byte(fixture(items.String())), strings.Repeat("😀", 256), 5)
	if err != nil || len(got.Results) == 0 {
		t.Fatal(got, err)
	}
	data, _ := json.Marshal(got)
	if len(data) >= 16384 {
		t.Fatal(len(data))
	}
	for _, r := range got.Results {
		if utf8.RuneCountInString(r.Title) > 100 || utf8.RuneCountInString(r.Snippet) > 240 || len(r.URL) > 1024 {
			t.Fatal(r)
		}
	}
	if _, err := parseResponse([]byte(fixture(items.String())+"<bad"), "query", 1); err != ErrInvalidResponse {
		t.Fatal(err)
	}
}
func TestHTTP(t *testing.T) {
	for _, status := range []int{204, 400, 434, 500, 503} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); io.WriteString(w, "secret body") })
		if _, err := c.Search(context.Background(), "secret query", 3); err != ErrHTTPStatus {
			t.Fatal(err)
		}
	}
	for _, location := range []string{"https://unknown.example/secret", "/again"} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, location, 302) })
		if _, err := c.Search(context.Background(), "secret", 3); err != ErrRedirect {
			t.Fatal(err)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.Close()
	c := &Client{apiKey: "test-key", endpoint: s.URL, httpClient: s.Client()}
	if _, err := c.Search(context.Background(), "secret", 3); err != ErrTransport {
		t.Fatal(err)
	}
}
func TestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New("test-key").Search(ctx, "query", 3); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() })
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Search(ctx, "secret", 3); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	started := make(chan struct{})
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go func() { <-started; cancel() }()
	if _, err := c.Search(ctx, "secret", 3); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
