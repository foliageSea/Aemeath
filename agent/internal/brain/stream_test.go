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
	"sync/atomic"
	"testing"
	"time"
)

func sseChunk(w http.ResponseWriter, delta map[string]any, finish any) {
	w.Header().Set("Content-Type", "text/event-stream")
	data, _ := json.Marshal(map[string]any{"id": "stream-test", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}
func sseEnd(w http.ResponseWriter) {
	sseChunk(w, map[string]any{}, "stop")
	io.WriteString(w, "data: [DONE]\n\n")
	w.(http.Flusher).Flush()
}

// The server cannot finish until the test observes the first callback. This
// proves streaming at the HTTP/Eino boundary without relying on elapsed sleeps.
func TestReplyStreamFirstDeltaBeforeCompletion(t *testing.T) {
	for _, tools := range []bool{false, true} {
		t.Run(fmt.Sprint("tools=", tools), func(t *testing.T) {
			first := make(chan string, 1)
			release := make(chan struct{})
			var requests atomic.Int32
			host := &testHost{}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				n := requests.Add(1)
				if tools && n <= 2 {
					msgs := req["messages"].([]any)
					selector := msgs[len(msgs)-1].(map[string]any)
					if selector["role"] != "system" || !strings.Contains(selector["content"].(string), "READY") || req["max_tokens"] != float64(512) {
						t.Error("selector must request only tool decisions with bounded tokens")
					}
				}
				if tools && n == 1 {
					if req["stream"] == true || lenOrZero(req["tools"]) != 7 {
						t.Error("tool decision must be completed before speech")
					}
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": "我正在切换页面，不应朗读。", "reasoning_content": "private plan", "tool_calls": []any{call("p", "set_page", `{"page":"voice"}`)}}}}})
					return
				}
				if tools && n == 2 {
					if req["stream"] == true {
						t.Error("tool decision unexpectedly streamed")
					}
					var found bool
					for _, v := range req["messages"].([]any) {
						if v.(map[string]any)["role"] == "tool" {
							found = true
						}
					}
					if !found {
						t.Error("ReAct lost tool result")
					}
					replyJSON(w, "decision text must not be spoken")
					return
				}
				if req["stream"] != true || lenOrZero(req["tools"]) != 0 || req["tool_choice"] != "none" {
					t.Errorf("final stream must forbid tools: %v", req)
				}
				msgs := req["messages"].([]any)
				for _, v := range msgs {
					m := v.(map[string]any)
					if text, ok := m["content"].(string); ok && strings.Contains(text, "READY") {
						t.Error("selector instruction leaked into final input")
					}
				}
				if tools {
					toolResult := msgs[len(msgs)-1].(map[string]any)
					if toolResult["role"] != "tool" || toolResult["tool_call_id"] != "p" || toolResult["content"] != "切页指令已发送；协议没有当前页面回执，不能确认设备实际已切换。" {
						t.Errorf("final stream lost exact tool result: %v", toolResult)
					}
				}
				sseChunk(w, map[string]any{"role": "assistant", "reasoning_content": "private reasoning"}, nil)
				sseChunk(w, map[string]any{"content": "第一句话。"}, nil)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				sseChunk(w, map[string]any{"content": "第二句话。"}, nil)
				sseEnd(w)
			}))
			defer s.Close()
			e := newEngine(t, s, tools, host)
			type streamResult struct {
				text string
				err  error
			}
			done := make(chan streamResult, 1)
			var spoken strings.Builder
			go func() {
				text, err := e.ReplyStream(context.Background(), "切到语音页", func(delta string) error {
					spoken.WriteString(delta)
					if spoken.Len() == len(delta) {
						first <- delta
					}
					return nil
				})
				done <- streamResult{text, err}
			}()
			select {
			case delta := <-first:
				if delta != "第一句话。" {
					t.Errorf("unsafe first delta %q", delta)
				}
			case result := <-done:
				close(release)
				t.Fatalf("completed before first delta: %+v", result)
			case <-time.After(3 * time.Second):
				close(release)
				t.Fatal("first delta waited for complete response")
			}
			select {
			case result := <-done:
				close(release)
				t.Fatalf("finished before server released: %+v", result)
			default:
			}
			close(release)
			result := <-done
			if result.err != nil || result.text != "第一句话。第二句话。" || spoken.String() != result.text {
				t.Fatalf("result=%+v spoken=%q", result, spoken.String())
			}
			if len(e.history) != 2 || e.history[1].Content != result.text {
				t.Fatal("final history not committed")
			}
			want := int32(1)
			if tools {
				want = 3
			}
			if requests.Load() != want {
				t.Fatalf("requests=%d", requests.Load())
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			if tools && (host.writes != 1 || host.page != "voice") {
				t.Fatalf("tool executed incorrectly: %+v", host)
			}
		})
	}
}

func TestReplyStreamFailureNeverReplaysTools(t *testing.T) {
	for _, failure := range []string{"unsupported", "partial", "abrupt", "length", "unexpected_tool"} {
		t.Run(failure, func(t *testing.T) {
			var requests atomic.Int32
			host := &testHost{}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				n := requests.Add(1)
				if n == 1 {
					callsJSON(w, []any{call("p", "set_page", `{"page":"voice"}`)})
					return
				}
				if n == 2 {
					replyJSON(w, "ready")
					return
				}
				if req["stream"] != true {
					t.Error("stream failure retried with full completion")
				}
				if failure == "unsupported" {
					w.WriteHeader(400)
					io.WriteString(w, `{"error":{"message":"secret-key streaming unsupported"}}`)
					return
				}
				if failure == "unexpected_tool" {
					sseChunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "bad", "type": "function", "function": map[string]any{"name": "set_page", "arguments": `{"page":"quota"}`}}}}, nil)
					sseEnd(w)
					return
				}
				sseChunk(w, map[string]any{"role": "assistant", "content": "已经发出指令。"}, nil)
				switch failure {
				case "partial":
					io.WriteString(w, "data: {\"error\":{\"message\":\"secret-key failure\"}}\n\n")
				case "length":
					sseChunk(w, map[string]any{}, "length")
					io.WriteString(w, "data: [DONE]\n\n")
				case "abrupt": // EOF without a finish_reason must not commit history.
				}
			}))
			defer s.Close()
			e := newEngine(t, s, true, host)
			spoken := ""
			text, err := e.ReplyStream(context.Background(), "切页", func(delta string) error { spoken += delta; return nil })
			if err == nil || text != "" || strings.Contains(err.Error(), "secret-key") {
				t.Fatalf("text=%q err=%v", text, err)
			}
			if failure == "unexpected_tool" && spoken != "" {
				t.Fatalf("tool args escaped: %q", spoken)
			}
			if len(e.history) != 0 || requests.Load() != 3 {
				t.Fatalf("history=%d requests=%d", len(e.history), requests.Load())
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			if host.writes != 1 || host.page != "voice" {
				t.Fatalf("tool replayed: %+v", host)
			}
		})
	}
}

func TestReplyStreamCancelClearCallbackAndSerialGate(t *testing.T) {
	for _, stop := range []string{"cancel", "clear", "callback"} {
		t.Run(stop, func(t *testing.T) {
			first := make(chan struct{}, 1)
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				readRequest(t, r)
				if requests.Add(1) > 1 {
					replyJSON(w, "next reply")
					return
				}
				sseChunk(w, map[string]any{"role": "assistant", "content": "第一句。"}, nil)
				<-r.Context().Done()
			}))
			defer s.Close()
			e := newEngine(t, s, false, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			callbackErr := errors.New("consumer stopped")
			done := make(chan error, 1)
			go func() {
				_, err := e.ReplyStream(ctx, "first", func(string) error {
					first <- struct{}{}
					if stop == "callback" {
						return callbackErr
					}
					return nil
				})
				done <- err
			}()
			select {
			case <-first:
			case <-time.After(3 * time.Second):
				t.Fatal("no first delta")
			}
			if stop != "callback" {
				queued, cancelQueued := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancelQueued()
				if _, err := e.Reply(queued, "queued"); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("gate bypassed: %v", err)
				}
				if stop == "cancel" {
					cancel()
				} else {
					e.Clear()
				}
			}
			select {
			case err := <-done:
				want := error(context.Canceled)
				if stop == "callback" {
					want = callbackErr
				}
				if !errors.Is(err, want) {
					t.Fatalf("err=%v want=%v", err, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stream did not stop")
			}
			if len(e.history) != 0 {
				t.Fatal("failed stream entered history")
			}
			if _, err := e.Reply(context.Background(), "next"); err != nil {
				t.Fatal("gate not released", err)
			}
		})
	}
}

func TestReplyStreamHistoryBoundAndClear(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		n := int(requests.Add(1)) - 1
		prior := n
		if prior > 9 {
			prior = 9
		}
		if n == 12 {
			prior = 0
		}
		msgs := req["messages"].([]any)
		if len(msgs) != 2+2*prior {
			t.Errorf("request %d messages=%d", n, len(msgs))
		}
		if n == 11 && msgs[1].(map[string]any)["content"] != "turn-2" {
			t.Error("old history not trimmed")
		}
		sseChunk(w, map[string]any{"role": "assistant", "content": "回复。"}, nil)
		sseEnd(w)
	}))
	defer s.Close()
	e := newEngine(t, s, false, nil)
	for n := 0; n < 12; n++ {
		if _, err := e.ReplyStream(context.Background(), fmt.Sprintf("turn-%d", n), nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.history) != 20 {
		t.Fatalf("history=%d", len(e.history))
	}
	e.Clear()
	if _, err := e.ReplyStream(context.Background(), "new", nil); err != nil {
		t.Fatal(err)
	}
	if len(e.history) != 2 {
		t.Fatal("clear failed")
	}
}

func TestReplyStreamToolDecisionLimits(t *testing.T) {
	for _, mode := range []string{"loop", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			host := &testHost{}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				if req["stream"] == true {
					t.Error("invalid selector should not reach final stream")
				}
				n := requests.Add(1)
				if mode == "loop" {
					callsJSON(w, []any{call(fmt.Sprint(n), "get_metrics", "{}")})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "length", "message": map[string]any{"role": "assistant", "tool_calls": []any{call("p", "set_page", `{"page":"voice"}`)}}}}})
			}))
			defer s.Close()
			e := newEngine(t, s, true, host)
			_, err := e.ReplyStream(context.Background(), "test", func(string) error { t.Error("selector leaked into callback"); return nil })
			if err == nil || len(e.history) != 0 {
				t.Fatalf("err=%v history=%d", err, len(e.history))
			}
			if mode == "truncated" {
				assertFailure(t, err, "tool_decision", "decision_truncated", 0)
			} else {
				assertFailure(t, err, "agent", "step_limit", 0)
			}
			if mode == "loop" && (requests.Load() < 2 || requests.Load() > maxSteps) {
				t.Fatalf("unbounded model calls: %d", requests.Load())
			}
			if mode == "truncated" && requests.Load() != 1 {
				t.Fatalf("truncated decision retried: %d", requests.Load())
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			if host.writes != 0 {
				t.Fatal("truncated selector executed tool")
			}
		})
	}
}

func TestReplyStreamLimits(t *testing.T) {
	for _, mode := range []string{"text", "wire", "fanout", "empty"} {
		t.Run(mode, func(t *testing.T) {
			host := &testHost{}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				readRequest(t, r)
				if mode == "fanout" {
					calls := make([]any, 9)
					for i := range calls {
						calls[i] = call(fmt.Sprint(i), "set_page", `{"page":"voice"}`)
					}
					callsJSON(w, calls)
					return
				}
				switch mode {
				case "text":
					sseChunk(w, map[string]any{"content": strings.Repeat("x", maxTextBytes+1)}, nil)
				case "wire":
					sseChunk(w, map[string]any{"reasoning_content": strings.Repeat("x", (1<<20)+1)}, nil)
				}
				sseEnd(w)
			}))
			defer s.Close()
			e := newEngine(t, s, mode == "fanout", host)
			spoken := 0
			_, err := e.ReplyStream(context.Background(), "test", func(delta string) error { spoken += len(delta); return nil })
			if err == nil || len(e.history) != 0 || spoken != 0 {
				t.Fatalf("err=%v history=%d spoken=%d", err, len(e.history), spoken)
			}
			if host.writes != 0 {
				t.Fatal("fanout executed")
			}
		})
	}
}
