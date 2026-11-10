package brain

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
)

type testHost struct {
	mu         sync.Mutex
	page       string
	on         bool
	brightness int
	writes     int
	locks      int
}

func (h *testHost) LockComputer(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.locks++
	return nil
}

func (*testHost) Metrics() string { return `{"cpu":12}` }
func (*testHost) Quota() string   { return `{"remaining":42}` }
func (h *testHost) SetPage(ctx context.Context, page string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.page = page
	h.writes++
	return nil
}
func (h *testHost) SetScreen(ctx context.Context, on bool, brightness int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.on = on
	h.brightness = brightness
	h.writes++
	return nil
}

func replyJSON(w http.ResponseWriter, text string) {
	json.NewEncoder(w).Encode(map[string]any{"id": "chatcmpl-test", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}}})
}
func callsJSON(w http.ResponseWriter, calls []any) {
	json.NewEncoder(w).Encode(map[string]any{"id": "chatcmpl-tools", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": calls}}}})
}
func call(id, name, args string) any {
	return map[string]any{"id": id, "type": "function", "function": map[string]string{"name": name, "arguments": args}}
}
func readRequest(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Error(err)
	}
	return req
}
func newEngine(t *testing.T, s *httptest.Server, tools bool, host Host) *Engine {
	t.Helper()
	e, err := New(context.Background(), Config{BaseURL: s.URL + "/v1", APIKey: "secret-key", Model: "mock-model", Tools: tools, Persona: "测试助手"}, host)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRealEinoToolCalls(t *testing.T) {
	h := &testHost{}
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret-key" {
			t.Errorf("bad request %s", r.URL.Path)
		}
		req := readRequest(t, r)
		if req["model"] != "mock-model" {
			t.Error("model not forwarded")
		}
		if len(req["tools"].([]any)) != 6 {
			t.Error("expected six real Eino tools")
		}
		if requests.Add(1) == 1 {
			callsJSON(w, []any{call("m", "get_metrics", "{}"), call("q", "get_quota", "{}"), call("p", "set_page", `{"page":"metrics"}`), call("s", "set_screen", `{"on":true,"brightness":37}`)})
			return
		}
		tools := map[string]string{}
		for _, item := range req["messages"].([]any) {
			m := item.(map[string]any)
			if m["role"] == "tool" {
				tools[m["tool_call_id"].(string)] = m["content"].(string)
			}
		}
		if len(tools) != 4 || !strings.Contains(tools["m"], "cpu") || !strings.Contains(tools["q"], "42") || tools["p"] == "" || tools["s"] == "" {
			t.Errorf("missing tool results %v", tools)
		}
		replyJSON(w, "CPU 使用率为 12%，已切换页面并设置亮度。")
	}))
	defer s.Close()
	e := newEngine(t, s, true, h)
	got, err := e.Reply(context.Background(), "查看状态并打开指标页，亮度设为37")
	if err != nil || !strings.Contains(got, "12%") {
		t.Fatalf("%s %v", got, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("model calls=%d", requests.Load())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.writes != 2 || h.page != "metrics" || !h.on || h.brightness != 37 {
		t.Fatalf("host=%+v", h)
	}
	if len(e.history) != 2 || len(e.history[1].ToolCalls) != 0 {
		t.Fatal("history must contain one complete text turn")
	}
}
func TestPlainChatHistoryBoundAndClear(t *testing.T) {
	requestNo := 0
	cleared := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		if lenOrZero(req["tools"]) != 0 {
			t.Error("plain mode sent tools")
		}
		msgs := req["messages"].([]any)
		prior := requestNo
		if prior > 9 {
			prior = 9
		}
		want := 2 + prior*2
		if cleared {
			want = 2
		}
		if len(msgs) != want {
			t.Errorf("request %d messages=%d want=%d", requestNo, len(msgs), want)
		}
		if msgs[0].(map[string]any)["content"] != "测试助手" {
			t.Error("persona missing")
		}
		if requestNo == 11 && !cleared && msgs[1].(map[string]any)["content"] != "turn-2" {
			t.Error("oldest history not trimmed")
		}
		requestNo++
		replyJSON(w, "回复")
	}))
	defer s.Close()
	e := newEngine(t, s, false, nil)
	for i := 0; i < 12; i++ {
		if _, err := e.Reply(context.Background(), fmt.Sprintf("turn-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.history) != 20 {
		t.Fatalf("history=%d", len(e.history))
	}
	e.Clear()
	cleared = true
	if _, err := e.Reply(context.Background(), "new conversation"); err != nil {
		t.Fatal(err)
	}
	if len(e.history) != 2 {
		t.Fatal("Clear failed")
	}
}
func lenOrZero(v any) int {
	if v == nil {
		return 0
	}
	return len(v.([]any))
}

func TestCancellationDuringModelAndQueuedReply(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
			replyJSON(w, "done")
		}
	}))
	defer s.Close()
	e := newEngine(t, s, true, &testHost{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.Reply(ctx, "first"); done <- err }()
	<-entered
	queued, cancelQueued := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelQueued()
	if _, err := e.Reply(queued, "queued"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued=%v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("request not canceled")
	}
	if len(e.history) != 0 {
		t.Fatal("canceled history committed")
	}
}
func TestClearInvalidatesInFlightHistory(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		close(entered)
		<-release
		replyJSON(w, "old reply")
	}))
	defer s.Close()
	e := newEngine(t, s, false, nil)
	done := make(chan error, 1)
	go func() { _, err := e.Reply(context.Background(), "old"); done <- err }()
	<-entered
	e.Clear()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(e.history) != 0 {
		t.Fatal("Clear repopulated by obsolete reply")
	}
}
func TestReActMaxStepStopsToolLoop(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		n := requests.Add(1)
		callsJSON(w, []any{call(fmt.Sprint(n), "get_metrics", "{}")})
	}))
	defer s.Close()
	e := newEngine(t, s, true, &testHost{})
	_, err := e.Reply(context.Background(), "loop")
	if err == nil {
		t.Fatal("unbounded loop accepted")
	}
	if requests.Load() > maxSteps || requests.Load() < 2 {
		t.Fatalf("calls=%d", requests.Load())
	}
	if len(e.history) != 0 {
		t.Fatal("failed turn retained")
	}
}
func TestModelErrorsRedactedAndResponsesBounded(t *testing.T) {
	for name, body := range map[string]string{"provider": `{"error":{"message":"secret-key Authorization Bearer leaked","type":"error","code":"invalid_api_key"}}`, "oversized": strings.Repeat("x", (1<<20)+1), "malformed": "secret-key"} {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				readRequest(t, r)
				if name == "provider" {
					w.WriteHeader(401)
				}
				io.WriteString(w, body)
			}))
			defer s.Close()
			e := newEngine(t, s, false, nil)
			_, err := e.Reply(context.Background(), "test")
			if err == nil || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "Authorization") {
				t.Fatalf("unsafe error=%v", err)
			}
			if len(e.history) != 0 {
				t.Fatal("failed history retained")
			}
		})
	}
}
func TestInvalidToolArgumentsHaveNoSideEffects(t *testing.T) {
	h := &testHost{}
	for _, tt := range []struct{ name, args string }{{"set_page", `{"page":"bad"}`}, {"set_screen", `{"on":true,"brightness":101}`}, {"set_screen", `{"brightness":10}`}, {"set_screen", `{"on":true,"brightness":10,"extra":true}`}, {"get_metrics", "null"}} {
		t.Run(tt.name+tt.args, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				readRequest(t, r)
				callsJSON(w, []any{call("invalid", tt.name, tt.args)})
			}))
			defer s.Close()
			e := newEngine(t, s, true, h)
			if _, err := e.Reply(context.Background(), "test"); err == nil {
				t.Fatal("invalid tool succeeded")
			}
		})
	}
	if h.writes != 0 {
		t.Fatal("invalid command changed device")
	}
}
func TestNewValidationAndPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(ctx, Config{Model: "m"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{}, {Model: "m", BaseURL: "http://user:secret-key@host"}, {Model: "m", BaseURL: "http://host?key=secret-key"}, {Model: "m", Tools: true}, {Model: "m", Persona: strings.Repeat("x", maxTextBytes+1)}} {
		if _, err := New(context.Background(), cfg, nil); err == nil || strings.Contains(err.Error(), "secret-key") {
			t.Fatalf("validation=%v", err)
		}
	}
}
