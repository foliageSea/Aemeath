package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
)

func TestToolObserverOutcomesAndIndependentIDs(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "canceled", "ok_false"} {
		t.Run(outcome, func(t *testing.T) {
			var events []ToolCall
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = WithToolObserver(ctx, func(call ToolCall) { events = append(events, call) })
			executions := 0
			expected := errors.New("private provider error sk-secret")
			tool := &deviceTool{info: &schema.ToolInfo{Name: "set_screen"}, run: func(context.Context, string) (string, error) {
				executions++
				if len(events) != executions*2-1 || events[len(events)-1].Status != "running" {
					t.Error("start must precede real invocation")
				}
				switch outcome {
				case "failed":
					return "", expected
				case "canceled":
					cancel()
					return "", context.Canceled
				case "ok_false":
					return `{"ok":false,"message":"private sk-secret"}`, nil
				}
				return "private result sk-secret", nil
			}}
			result, err := tool.InvokableRun(ctx, `{"brightness":42,"on":true}`)
			if outcome == "failed" && err != expected {
				t.Fatalf("changed error: %v", err)
			}
			if outcome == "succeeded" && result != "private result sk-secret" {
				t.Fatal("changed model result")
			}
			if outcome == "ok_false" && err != nil {
				t.Fatal("ok:false must remain a normal model tool result")
			}
			want := outcome
			if want == "ok_false" {
				want = "failed"
			}
			if executions != 1 || len(events) != 2 || events[0].ID != 1 || events[1].ID != 1 || events[0].Status != "running" || events[1].Status != want {
				t.Fatalf("bad events: %+v", events)
			}
			if events[1].Arguments != `{"on":true,"brightness":42}` || events[1].Millis < 1 {
				t.Fatalf("bad display details: %+v", events[1])
			}
			for _, event := range events {
				data, _ := json.Marshal(event)
				if strings.Contains(string(data), "private") || strings.Contains(string(data), "sk-secret") || len(data) > 4096 || !utf8.Valid(data) {
					t.Fatalf("unsafe event: %s", data)
				}
			}
			var isolated []ToolCall
			independent := WithToolObserver(context.Background(), func(c ToolCall) { isolated = append(isolated, c) })
			tool.run = func(context.Context, string) (string, error) { return "", nil }
			tool.InvokableRun(independent, "{}")
			if isolated[0].ID != 1 {
				t.Fatal("new observer must reset IDs")
			}
		})
	}
}

func TestToolObserverBoundsAndUnknownArguments(t *testing.T) {
	for _, args := range []string{`{"page":"sk-secret"}`, `{"page":"voice","api_key":"sk-secret"}`, strings.Repeat("密", 4096), string([]byte{255})} {
		var events []ToolCall
		ctx := WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) })
		selected := deviceTools(&testHost{})[3].(*deviceTool)
		selected.InvokableRun(ctx, args)
		if len(events) != 2 || events[1].Status != "failed" || events[0].Arguments != "" {
			t.Fatalf("unsafe args summary: %+v", events)
		}
	}
}

func TestRealReActToolObserverContextAndSpeechSeparation(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tools := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/tools=%v", streaming, tools), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					req := readRequest(t, r)
					n := requests.Add(1)
					if tools && n == 1 {
						callsJSON(w, []any{call("provider-random-id", "set_page", `{"page":"voice"}`), call("provider-second", "get_metrics", "{}")})
						return
					}
					if req["stream"] == true {
						sseChunk(w, map[string]any{"role": "assistant", "content": "最终答复。"}, nil)
						sseEnd(w)
					} else {
						replyJSON(w, "最终答复。")
					}
				}))
				defer server.Close()
				host := &testHost{}
				engine := newEngine(t, server, tools, host)
				var mu sync.Mutex
				var events []ToolCall
				ctx := WithToolObserver(context.Background(), func(c ToolCall) { mu.Lock(); defer mu.Unlock(); events = append(events, c) })
				var text string
				var err error
				if streaming {
					text, err = engine.ReplyStream(ctx, "切换页面并读取指标", func(string) error { return nil })
				} else {
					text, err = engine.Reply(ctx, "切换页面并读取指标")
				}
				if err != nil || text != "最终答复。" {
					t.Fatalf("text=%q error=%v", text, err)
				}
				mu.Lock()
				defer mu.Unlock()
				if !tools {
					if len(events) != 0 {
						t.Fatalf("no-tools emitted %+v", events)
					}
					return
				}
				if len(events) != 4 {
					t.Fatalf("observer context lost through ReAct: %+v", events)
				}
				for i, event := range events {
					status := "running"
					if i%2 == 1 {
						status = "succeeded"
					}
					if event.ID != uint64(i/2+1) || event.Status != status {
						t.Fatalf("wrong order: %+v", events)
					}
				}
				if host.writes != 1 {
					t.Fatalf("tool repeated %d times", host.writes)
				}
			})
		}
	}
}
