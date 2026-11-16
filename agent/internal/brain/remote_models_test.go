package brain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/netproxy"
)

func TestFetchRemoteModelsSuccessAndPaths(t *testing.T) {
	for _, path := range []string{"", "/", "/v1", "/v1///", "/custom/api/v2/", "/escaped%20path/"} {
		t.Run(path, func(t *testing.T) {
			for _, key := range []string{"", "  test-secret  "} {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.EscapedPath() != strings.TrimRight(path, "/")+"/models" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
					}
					wantAuth := ""
					if strings.TrimSpace(key) != "" {
						wantAuth = "Bearer test-secret"
					}
					if r.Header.Get("Authorization") != wantAuth || r.Header.Get("Accept") != "application/json" {
						t.Error("incorrect authentication or Accept header")
					}
					io.WriteString(w, `{"object":"list","data":[{"id":"z-model"},{"id":" a-model "},{"id":""},{"id":"  "},{"id":"a-model"},{"id":"z-model"},{"id":"provider/model"},{}]}`)
				}))
				got, err := FetchRemoteModels(context.Background(), server.URL+path, key)
				server.Close()
				if err != nil || !reflect.DeepEqual(got, []string{"a-model", "provider/model", "z-model"}) {
					t.Fatalf("got %v, %v", got, err)
				}
			}
		})
	}
}

func TestFetchRemoteModelsEmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"data":[]}`) }))
	defer server.Close()
	got, err := FetchRemoteModels(context.Background(), server.URL, "")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestFetchRemoteModelsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", 401, "test-secret raw-private-response", "HTTP status 401"},
		{"server_error", 503, "raw-private-response", "HTTP status 503"},
		{"no_content", 204, "", "HTTP status 204"},
		{"html", 200, "<html>raw-private-response</html>", "invalid response format"},
		{"missing_data", 200, `{"error":"test-secret raw-private-response"}`, "invalid response format"},
		{"null", 200, "null", "invalid response format"},
		{"null_data", 200, `{"data":null}`, "invalid response format"},
		{"wrong_data_type", 200, `{"data":{}}`, "invalid response format"},
		{"wrong_id_type", 200, `{"data":[{"id":123}]}`, "invalid response format"},
		{"truncated", 200, `{"data":[`, "invalid response format"},
		{"trailing_json", 200, `{"data":[]} {"secret":"raw-private-response"}`, "invalid response format"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			got, err := FetchRemoteModels(context.Background(), server.URL, "test-secret")
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, %v", got, err)
			}
			assertRemoteModelsSafeError(t, err)
		})
	}
}

func TestFetchRemoteModelsResponseSizeLimit(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprint("chunked=", chunked), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if chunked {
					w.(http.Flusher).Flush()
				} else {
					w.Header().Set("Content-Length", fmt.Sprint(remoteModelsMaxBytes+1))
				}
				io.WriteString(w, strings.Repeat(" ", remoteModelsMaxBytes+1))
			}))
			defer server.Close()
			got, err := FetchRemoteModels(context.Background(), server.URL, "test-secret")
			if got != nil || err == nil || !strings.Contains(err.Error(), "response too large") {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestFetchRemoteModelsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchRemoteModels(ctx, "http://127.0.0.1:1", "test-secret"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	for _, bodyStarted := range []bool{false, true} {
		t.Run(fmt.Sprint("body=", bodyStarted), func(t *testing.T) {
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if bodyStarted {
					io.WriteString(w, `{"data":[`)
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := FetchRemoteModels(ctx, server.URL, "test-secret"); done <- err }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("request did not arrive")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not stop request")
			}
		})
	}
}

func TestFetchRemoteModelsDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := FetchRemoteModels(ctx, server.URL, "test-secret"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestFetchRemoteModelsDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{"data":[]}`) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/raw-private-response", http.StatusFound)
	}))
	defer server.Close()
	_, err := FetchRemoteModels(context.Background(), server.URL, "test-secret")
	if err == nil || !strings.Contains(err.Error(), "302") || calls.Load() != 0 {
		t.Fatalf("err=%v target calls=%d", err, calls.Load())
	}
	assertRemoteModelsSafeError(t, err)
}

func TestRemoteModelsEndpoint(t *testing.T) {
	for _, base := range []string{"", "  ", "https://api.openai.com/v1///"} {
		got, err := remoteModelsEndpoint(base)
		if err != nil || got != "https://api.openai.com/v1/models" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	for _, base := range []string{"relative/v1", "//example.com/v1", "ftp://example.com", "http://", "http:///v1", "http://:123/v1", "https://test-secret@example.com", "https://example.com?key=test-secret", "https://example.com?", "https://example.com/#test-secret", "https://example.com/#", "http://example.com/%test-secret", "http://example.com:bad"} {
		t.Run(base, func(t *testing.T) {
			if _, err := FetchRemoteModels(context.Background(), base, "test-secret"); err == nil {
				t.Fatal("accepted invalid base URL")
			} else {
				assertRemoteModelsSafeError(t, err)
			}
		})
	}
}

func TestFetchRemoteModelsUsesContextProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "http://provider.invalid/custom/v1/models" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected proxy request: %s", r.URL)
		}
		io.WriteString(w, `{"data":[{"id":"proxied"}]}`)
	}))
	defer proxy.Close()
	router, err := netproxy.New(netproxy.Config{Mode: netproxy.ModeProxy, ProxyURL: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	got, err := FetchRemoteModels(netproxy.WithRouter(context.Background(), router), "http://provider.invalid/custom/v1", "test-secret")
	if err != nil || !reflect.DeepEqual(got, []string{"proxied"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestFetchRemoteModelsTransportErrorsAreRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid header should not reach server") }))
	defer server.Close()
	_, err := FetchRemoteModels(context.Background(), server.URL+"/raw-private-response", "test-secret\ninvalid")
	if err == nil {
		t.Fatal("expected invalid authorization header error")
	}
	assertRemoteModelsSafeError(t, err)
}

func assertRemoteModelsSafeError(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{"test-secret", "raw-private-response"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked private content: %v", err)
		}
	}
}
