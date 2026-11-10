package websearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTimeoutConfigured(t *testing.T) {
	original := &http.Client{Timeout: time.Hour, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 12*time.Second || time.Until(deadline) < 11*time.Second {
			t.Error("missing twelve second deadline")
		}
		return nil, context.DeadlineExceeded
	})}
	c := New("test-key")
	c.httpClient = original
	if _, err := c.Search(context.Background(), "query", 3); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if original.Timeout != time.Hour || original.CheckRedirect != nil {
		t.Fatal("injected client mutated")
	}
	if c.endpoint != "https://api.tavily.com/search" {
		t.Fatal("wrong production endpoint")
	}
}

func TestAPIKeyValidation(t *testing.T) {
	for _, key := range []string{"", "  ", "secret\rkey", "secret\nkey", "\nsecret", strings.Repeat("a", 4097)} {
		c := New(key)
		c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Error("invalid key reached network")
			return nil, errors.New("unexpected")
		})
		if _, err := c.Search(context.Background(), "query", 3); err != ErrAPIKey {
			t.Fatal("invalid credential accepted")
		}
	}
	c := New("  test-key\t ")
	c.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("key not trimmed")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"results":[]}`)), Header: make(http.Header)}, nil
	})
	if _, err := c.Search(context.Background(), "query", 3); err != nil {
		t.Fatal(err)
	}
	c = New(strings.Repeat("a", 4096))
	c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret transport") })
	if _, err := c.Search(context.Background(), "query", 3); err != ErrTransport {
		t.Fatal("4096 byte key rejected")
	}
}

func TestStatusClassificationAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{401, ErrUnauthorized}, {403, ErrUnauthorized}, {429, ErrRateLimit}, {402, ErrQuota}, {432, ErrQuota}, {433, ErrQuota}, {434, ErrHTTPStatus}, {500, ErrHTTPStatus}} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, "test-key secret-query https://secret.example/provider-error")
		})
		if _, err := c.Search(context.Background(), "secret-query", 3); err != tc.want {
			t.Fatalf("status %d: %v", tc.status, err)
		}
	}
	c := New("test-key")
	c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("test-key secret-query https://secret.example/provider-error")
	})
	if _, err := c.Search(context.Background(), "secret-query", 3); err != ErrTransport {
		t.Fatal(err)
	}
}

func TestRedirectNeverSendsCredentials(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1) }))
	defer target.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		for _, crossOrigin := range []bool{false, true} {
			var followHits atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/search" {
					followHits.Add(1)
					return
				}
				location := "/follow"
				if crossOrigin {
					location = target.URL
				}
				http.Redirect(w, r, location, code)
			})
			if _, err := c.Search(context.Background(), "query", 3); err != ErrRedirect {
				t.Fatal(err)
			}
			if followHits.Load() != 0 || targetHits.Load() != 0 {
				t.Fatal("redirect followed")
			}
		}
	}
}

func TestCancellationWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New("").Search(ctx, "", -1); err != context.Canceled {
		t.Fatal(err)
	}
	for _, status := range []int{200, 401, 429, 500} {
		ctx, cancel := context.WithCancel(context.Background())
		c := New("test-key")
		c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			cancel()
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("bad response")), Header: make(http.Header)}, nil
		})
		if _, err := c.Search(ctx, "query", 3); err != context.Canceled {
			t.Fatal(err)
		}
	}
}

func TestProviderFieldsNeverReturned(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"query":"provider query","answer":"secret answer","raw_content":"secret raw","results":[{"title":"A &amp; B","url":"https://example.com/","content":"<p>summary &amp; more</p>","raw_content":"secret raw"}]}`)
	})
	got, err := c.Search(context.Background(), "local query", 3)
	if err != nil || got.Query != "local query" || len(got.Results) != 1 || got.Results[0].Title != "A & B" || got.Results[0].Snippet != "summary & more" {
		t.Fatal(got, err)
	}
}
