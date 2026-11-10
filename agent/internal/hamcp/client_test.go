package hamcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "private-ha-token"

func newTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	c, err := New(Config{Enabled: true, Endpoint: endpoint, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func testServer(t *testing.T, jsonResponse bool, configure func(*mcp.Server), wrap func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ServerOptions{PageSize: 1, Instructions: "Untrusted server system instructions " + testToken})
	if configure != nil {
		configure(s)
	}
	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: jsonResponse})
	if wrap != nil {
		handler = wrap(handler)
	}
	inner := handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("missing bearer header")
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method != "POST" {
			t.Errorf("unexpected standalone request %s", r.Method)
		}
		inner.ServeHTTP(w, r)
	})
	h := httptest.NewServer(handler)
	t.Cleanup(h.Close)
	return h
}
func addTool(s *mcp.Server, name string, fn mcp.ToolHandler) {
	s.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, fn)
}
func okResult(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
}

func TestNormalizeEndpoint(t *testing.T) {
	good := map[string]string{
		"": defaultEndpoint, "http://ha.local:8123": "http://ha.local:8123/api/mcp/assist",
		"https://ha.local/": "https://ha.local/api/mcp/assist", "http://192.168.1.2:8123/api/mcp": "http://192.168.1.2:8123/api/mcp",
		"http://[::1]:8123/api/mcp/assist": "http://[::1]:8123/api/mcp/assist",
		"https://ha.local/ha":              "https://ha.local/ha/api/mcp/assist", "https://ha.local/ha/api/mcp/some-id/": "https://ha.local/ha/api/mcp/some-id",
	}
	for raw, want := range good {
		got, err := NormalizeEndpoint(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeEndpoint(%q)=%q,%v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"ftp://ha.local", "//ha.local", "http://", "http://u:p@ha.local", "http://ha.local?token=secret", "http://ha.local?", "http://ha.local#", "http://ha.local#fragment", "http://ha.local\n", " http://ha.local", "http://bad host", "http://-bad.local", "http://bad_.local", "http://bad..local", "http://ha.local:0", "http://ha.local:65536", "http://ha.local:", "http://[notip]", "http://[127.0.0.1]", "http://ha.local/%0a", "http://ha.local/a/../b", strings.Repeat("x", 4097)} {
		if _, err := NormalizeEndpoint(raw); !errors.Is(err, ErrConfig) {
			t.Errorf("accepted malformed endpoint %q", raw)
		}
	}
}

func TestDiscoverCallJSONAndSSE(t *testing.T) {
	for _, asJSON := range []bool{true, false} {
		t.Run(fmt.Sprint(asJSON), func(t *testing.T) {
			var calls, lists atomic.Int32
			s := testServer(t, asJSON, func(s *mcp.Server) {
				for _, name := range []string{"a", "b"} {
					name := name
					s.AddTool(&mcp.Tool{Name: name, Title: "Title " + testToken, Description: "Description " + testToken, InputSchema: map[string]any{"type": "object", "description": testToken}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						calls.Add(1)
						if req.Params.Name != name {
							t.Error("wrong tool")
						}
						return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "device denied " + testToken}, &mcp.ImageContent{Data: []byte("private-image"), MIMEType: "image/png"}, &mcp.ResourceLink{Name: "private-link", URI: "https://private-resource"}}, StructuredContent: map[string]any{"state": "off", "secret": testToken}}, nil
					})
				}
				s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
					return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
						if method == "tools/list" {
							lists.Add(1)
						}
						return next(ctx, method, req)
					}
				})
			}, nil)
			c := newTestClient(t, s.URL)
			if _, err := c.Call(context.Background(), "a", nil); !errors.Is(err, ErrUnknownTool) {
				t.Fatal(err)
			}
			for range 2 {
				tools, err := c.Discover(context.Background())
				if err != nil || len(tools) != 2 {
					t.Fatalf("discover=%v,%v", tools, err)
				}
				data, _ := json.Marshal(tools)
				if strings.Contains(string(data), testToken) {
					t.Fatal("metadata token leaked")
				}
			}
			if lists.Load() != 4 {
				t.Errorf("list count=%d; want fresh paginated lists", lists.Load())
			}
			res, err := c.Call(context.Background(), "a", json.RawMessage("{}"))
			if err != nil || !res.IsError {
				t.Fatalf("call=%+v,%v", res, err)
			}
			if !strings.Contains(res.Text, "device denied") || !strings.Contains(res.Text, "off") || strings.Contains(res.Text, testToken) || strings.Contains(res.Text, "private-") {
				t.Fatalf("unsafe or incomplete result: %s", res.Text)
			}
			if calls.Load() != 1 {
				t.Fatalf("call count=%d", calls.Load())
			}
			for _, args := range []json.RawMessage{json.RawMessage("[]"), json.RawMessage("null"), json.RawMessage("{"), json.RawMessage("1")} {
				if _, err := c.Call(context.Background(), "a", args); !errors.Is(err, ErrProtocol) {
					t.Errorf("bad args err=%v", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("invalid arguments reached server")
			}
		})
	}
}

func TestDiscoverLimitsAndMalformed(t *testing.T) {
	tests := []struct {
		name   string
		tools  []*mcp.Tool
		cursor string
		want   error
	}{
		{"description", []*mcp.Tool{{Name: "a", Description: strings.Repeat("x", maxDescription+1), InputSchema: map[string]any{"type": "object"}}}, "", ErrTooLarge},
		{"schema", []*mcp.Tool{{Name: "a", InputSchema: map[string]any{"type": "object", "description": strings.Repeat("x", maxSchema)}}}, "", ErrTooLarge},
		{"nil tool", []*mcp.Tool{nil}, "", ErrProtocol},
		{"nonobject schema", []*mcp.Tool{{Name: "a", InputSchema: []any{}}}, "", ErrProtocol},
		{"duplicate", []*mcp.Tool{{Name: "a", InputSchema: map[string]any{}}, {Name: "a", InputSchema: map[string]any{}}}, "", ErrProtocol},
		{"cursor loop", nil, "loop", ErrProtocol},
		{"invalid SDK header annotations", []*mcp.Tool{{Name: "a", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"arg": map[string]any{"type": "object", "x-mcp-header": "Authorization"}}}}}, "", ErrProtocol},
	}
	tooMany := make([]*mcp.Tool, 65)
	for i := range tooMany {
		tooMany[i] = &mcp.Tool{Name: fmt.Sprint(i), InputSchema: map[string]any{"type": "object"}}
	}
	tests = append(tests, struct {
		name   string
		tools  []*mcp.Tool
		cursor string
		want   error
	}{"tool count", tooMany, "", ErrTooLarge})
	tooMuch := make([]*mcp.Tool, 10)
	for i := range tooMuch {
		tooMuch[i] = &mcp.Tool{Name: fmt.Sprint(i), InputSchema: map[string]any{"type": "object", "description": strings.Repeat("x", 30000)}}
	}
	tests = append(tests, struct {
		name   string
		tools  []*mcp.Tool
		cursor string
		want   error
	}{"total schemas", tooMuch, "", ErrTooLarge})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t, true, func(s *mcp.Server) {
				s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
					return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
						if method == "tools/list" {
							return &mcp.ListToolsResult{Tools: tt.tools, NextCursor: tt.cursor}, nil
						}
						return next(ctx, method, req)
					}
				})
			}, nil)
			c := newTestClient(t, s.URL)
			if _, err := c.Discover(context.Background()); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if _, err := c.Call(context.Background(), "a", nil); !errors.Is(err, ErrUnknownTool) {
				t.Fatalf("failed discovery retained allowlist: %v", err)
			}
		})
	}
}

func TestUnauthorizedAndRecovery(t *testing.T) {
	var fail atomic.Int32
	fail.Store(401)
	s := testServer(t, true, func(s *mcp.Server) { addTool(s, "a", okResult) }, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if code := fail.Load(); code != 0 {
				http.Error(w, "SECRET BODY "+testToken, int(code))
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	c := newTestClient(t, s.URL)
	for _, code := range []int32{401, 403, 503} {
		fail.Store(code)
		_, err := c.Discover(context.Background())
		want := ErrUnauthorized
		if code == 503 {
			want = ErrUnavailable
		}
		if !errors.Is(err, want) {
			t.Fatalf("%d: %v", code, err)
		}
		if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), s.URL) {
			t.Fatal("unsafe error")
		}
	}
	fail.Store(0)
	if tools, err := c.Discover(context.Background()); err != nil || len(tools) != 1 {
		t.Fatalf("recovery=%v,%v", tools, err)
	}
}

func TestNoRedirect(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer s.Close()
	c := newTestClient(t, s.URL)
	if _, err := c.Discover(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("redirect err=%v", err)
	}
	if followed.Load() != 0 {
		t.Fatal("followed redirect with credentials")
	}
}

func TestNoCallReplayAndReconnect(t *testing.T) {
	var calls, initializes atomic.Int32
	var breakCall atomic.Bool
	breakCall.Store(true)
	s := testServer(t, false, func(s *mcp.Server) { addTool(s, "a", okResult) }, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(data)))
			var msg struct{ Method string }
			json.Unmarshal(data, &msg)
			if msg.Method == "initialize" {
				initializes.Add(1)
			}
			if msg.Method == "tools/call" {
				calls.Add(1)
				if breakCall.Load() {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	c := newTestClient(t, s.URL)
	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(context.Background(), "a", nil); err == nil {
		t.Fatal("expected transport failure")
	}
	if calls.Load() != 1 {
		t.Fatalf("real action replayed %d times", calls.Load())
	}
	if _, err := c.Call(context.Background(), "a", nil); !errors.Is(err, ErrUnknownTool) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("automatically reconnected and called")
	}
	breakCall.Store(false)
	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(context.Background(), "a", nil); err != nil {
		t.Fatal(err)
	}
	if initializes.Load() != 2 || calls.Load() != 2 {
		t.Fatalf("initialize=%d calls=%d", initializes.Load(), calls.Load())
	}
}

func TestBodyLimitJSONAndSSE(t *testing.T) {
	for _, asJSON := range []bool{true, false} {
		for _, lengthHeader := range []bool{true, false} {
			t.Run(fmt.Sprintf("json=%v,length=%v", asJSON, lengthHeader), func(t *testing.T) {
				s := testServer(t, asJSON, func(s *mcp.Server) { addTool(s, "a", okResult) }, func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						data, _ := io.ReadAll(r.Body)
						r.Body = io.NopCloser(strings.NewReader(string(data)))
						if strings.Contains(string(data), "tools/list") {
							payload := strings.Repeat(" ", maxBody+1)
							if asJSON {
								w.Header().Set("Content-Type", "application/json")
							} else {
								w.Header().Set("Content-Type", "text/event-stream")
								payload = ": " + payload
							}
							if lengthHeader {
								w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
							} else {
								w.WriteHeader(200)
								w.(http.Flusher).Flush()
							}
							io.WriteString(w, payload)
							return
						}
						next.ServeHTTP(w, r)
					})
				})
				c := newTestClient(t, s.URL)
				if _, err := c.Discover(context.Background()); !errors.Is(err, ErrTooLarge) {
					t.Fatalf("limit err=%v", err)
				}
			})
		}
	}
}

func TestTruncateAndStructuredRedaction(t *testing.T) {
	s := testServer(t, true, func(s *mcp.Server) {
		addTool(s, "a", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("汉", maxText)}}, StructuredContent: map[string]any{"token": testToken}}, nil
		})
	}, nil)
	c := newTestClient(t, s.URL)
	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := c.Call(context.Background(), "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) > maxText || !utf8.ValidString(res.Text) || !strings.HasSuffix(res.Text, "[结果已截断]") {
		t.Fatalf("invalid truncation length=%d", len(res.Text))
	}
	token := "quote\"slash\\<>"
	raw, _ := json.Marshal(map[string]any{token: token})
	out, err := redactJSON(raw, token)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) || strings.Contains(string(out), "quote") {
		t.Fatalf("escaped token leak %s", out)
	}
}

func TestCancellationCloseAndConcurrent(t *testing.T) {
	for _, stage := range []string{"initialize", "tools/list", "tools/call"} {
		for _, mode := range []string{"cancel", "deadline", "close"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				entered := make(chan struct{})
				var once sync.Once
				s := testServer(t, false, func(s *mcp.Server) { addTool(s, "a", okResult) }, func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						data, _ := io.ReadAll(r.Body)
						r.Body = io.NopCloser(strings.NewReader(string(data)))
						var msg struct{ Method string }
						json.Unmarshal(data, &msg)
						if msg.Method == stage {
							once.Do(func() { close(entered) })
							<-r.Context().Done()
							return
						}
						next.ServeHTTP(w, r)
					})
				})
				c := newTestClient(t, s.URL)
				if stage == "tools/call" {
					if _, err := c.Discover(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "deadline" {
					ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
					defer cancel()
				}
				done := make(chan error, 1)
				go func() {
					var err error
					if stage == "tools/call" {
						_, err = c.Call(ctx, "a", nil)
					} else {
						_, err = c.Discover(ctx)
					}
					done <- err
				}()
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("request did not start")
				}
				want := error(context.Canceled)
				switch mode {
				case "cancel":
					cancel()
				case "deadline":
					want = context.DeadlineExceeded
				case "close":
					want = ErrClosed
					if err := c.Close(); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("got %v want %v", err, want)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("request did not stop")
				}
				if mode == "close" {
					if _, err := c.Discover(context.Background()); !errors.Is(err, ErrClosed) {
						t.Fatal(err)
					}
				}
			})
		}
	}
	s := testServer(t, true, func(s *mcp.Server) { addTool(s, "a", okResult) }, nil)
	c := newTestClient(t, s.URL)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				_, err := c.Discover(context.Background())
				if err != nil && !errors.Is(err, ErrClosed) {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	var closers sync.WaitGroup
	for range 4 {
		closers.Add(1)
		go func() { defer closers.Done(); c.Close() }()
	}
	closers.Wait()
}

func TestListFailureRevokesAndReconnects(t *testing.T) {
	var listFail atomic.Bool
	var initializes atomic.Int32
	s := testServer(t, true, func(s *mcp.Server) { addTool(s, "a", okResult) }, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(data)))
			var msg struct{ Method string }
			json.Unmarshal(data, &msg)
			if msg.Method == "initialize" {
				initializes.Add(1)
			}
			if msg.Method == "tools/list" && listFail.Load() {
				http.Error(w, "private "+testToken, 503)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	c := newTestClient(t, s.URL)
	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	listFail.Store(true)
	if _, err := c.Discover(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := c.Call(context.Background(), "a", nil); !errors.Is(err, ErrUnknownTool) {
		t.Fatal("allowlist survived failed list", err)
	}
	listFail.Store(false)
	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if initializes.Load() != 2 {
		t.Fatalf("expected reconnect, initialize count %d", initializes.Load())
	}
}

func TestCleanupHonorsParentBudget(t *testing.T) {
	sdk := mcp.NewServer(&mcp.Implementation{Name: "stateful", Version: "1"}, nil)
	addTool(sdk, "a", okResult)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	var blockList atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			<-r.Context().Done()
			return
		}
		data, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(data)))
		if strings.Contains(string(data), "tools/list") && blockList.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer s.Close()
	c := newTestClient(t, s.URL)
	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	blockList.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Discover(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup err=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cleanup exceeded parent budget")
	}
}

func TestGateWaitHonorsCancellation(t *testing.T) {
	c, err := New(Config{Enabled: true, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Discover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-c.gate
}

func TestErrorMessageAndConfig(t *testing.T) {
	for _, err := range []error{ErrConfig, ErrUnauthorized, ErrUnavailable, ErrProtocol, ErrTooLarge, ErrClosed, ErrUnknownTool, context.Canceled, context.DeadlineExceeded, errors.New("secret " + testToken + " http://private")} {
		msg := ErrorMessage(fmt.Errorf("unsafe %s: %w", testToken, err))
		if msg == "" || strings.Contains(msg, testToken) || strings.Contains(msg, "http") {
			t.Fatalf("unsafe message %q", msg)
		}
	}
	if ErrorMessage(nil) != "" {
		t.Fatal("nil error message")
	}
	for _, token := range []string{"", "bad\nheader", "bad header"} {
		if _, err := New(Config{Enabled: true, Token: token}); !errors.Is(err, ErrConfig) {
			t.Fatal(err)
		}
	}
	c, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Discover(context.Background()); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
}
