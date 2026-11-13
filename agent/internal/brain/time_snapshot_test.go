package brain

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTimeSnapshotPrompt(t *testing.T) {
	cases := []struct {
		name, zone, instant, local, utc, weekday string
	}{
		{"ShanghaiBeforeMidnight", "Asia/Shanghai", "2026-11-10T15:59:59Z", "2026-11-10T23:59:59+08:00", "2026-11-10T15:59:59Z", "星期二"},
		{"ShanghaiMidnight", "Asia/Shanghai", "2026-11-10T16:00:00Z", "2026-11-11T00:00:00+08:00", "2026-11-10T16:00:00Z", "星期三"},
		{"NewYorkBeforeSpringDST", "America/New_York", "2026-03-08T06:59:59Z", "2026-03-08T01:59:59-05:00", "2026-03-08T06:59:59Z", "星期日"},
		{"NewYorkAfterSpringDST", "America/New_York", "2026-03-08T07:00:00Z", "2026-03-08T03:00:00-04:00", "2026-03-08T07:00:00Z", "星期日"},
		{"NewYorkBeforeFallDST", "America/New_York", "2026-11-01T05:59:59Z", "2026-11-01T01:59:59-04:00", "2026-11-01T05:59:59Z", "星期日"},
		{"NewYorkAfterFallDST", "America/New_York", "2026-11-01T06:00:00Z", "2026-11-01T01:00:00-05:00", "2026-11-01T06:00:00Z", "星期日"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			location, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			now, err := time.Parse(time.RFC3339, tc.instant)
			if err != nil {
				t.Fatal(err)
			}
			now = now.In(location)
			prompt := timeSnapshotPrompt(now)
			assertTimeSnapshotFields(t, prompt, tc.local, tc.utc, tc.zone, tc.weekday)
			if again := timeSnapshotPrompt(now); again != prompt {
				t.Fatalf("same instant produced different snapshots:\n%s\n%s", prompt, again)
			}
		})
	}
}

func TestTimeSnapshotPromptUnnamedZoneFallback(t *testing.T) {
	for _, offset := range []int{(5*60 + 45) * 60, -(5*60 + 30) * 60} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.FixedZone("", offset))
			prompt := timeSnapshotPrompt(now)
			// An unnamed fixed zone must display its explicit numeric offset,
			// rather than guessing an IANA name or the machine's local zone.
			assertTimeSnapshotFields(t, prompt, now.Format(time.RFC3339), now.UTC().Format(time.RFC3339), "UTC"+now.Format("-07:00"), "星期三")
		})
	}
}

func assertTimeSnapshotFields(t *testing.T, prompt string, fields ...string) {
	t.Helper()
	for _, want := range append(fields, "本轮时间快照", "电脑系统时钟", "未进行外部校时") {
		if !strings.Contains(prompt, want) {
			t.Errorf("snapshot missing %q:\n%s", want, prompt)
		}
	}
	if strings.Count(prompt, "本轮时间快照：") != 1 {
		t.Errorf("snapshot must contain exactly one header: %q", prompt)
	}
	// Do not freeze the prose, but retain the ordinary-time/no-tool policy.
	if !strings.Contains(prompt, "快照") || !strings.Contains(prompt, "工具") {
		t.Errorf("snapshot lacks instructions to answer ordinary time questions without tools: %q", prompt)
	}
	noTool := false
	for _, wording := range []string{"无需", "不必", "不要", "不需要", "不调用"} {
		noTool = noTool || strings.Contains(prompt, wording)
	}
	if !noTool {
		t.Errorf("snapshot lacks a no-tool instruction: %q", prompt)
	}
}

// Every model request, including private tool selection, must receive just one
// snapshot appended to the original first system persona, never a history item.
func assertTimeSnapshotRequest(t *testing.T, req map[string]any, snapshot string) string {
	t.Helper()
	assertNoCurrentTimeToolRequest(t, req)
	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Error("request has no messages")
		return ""
	}
	first := messages[0].(map[string]any)
	system, _ := first["content"].(string)
	if first["role"] != "system" || !strings.HasPrefix(system, "测试助手") || !strings.HasSuffix(system, snapshot) {
		t.Errorf("snapshot not appended to first system persona: %#v", first)
	}
	count := 0
	for _, raw := range messages {
		message := raw.(map[string]any)
		content, _ := message["content"].(string)
		count += strings.Count(content, "本轮时间快照：")
	}
	if count != 1 {
		t.Errorf("request contains %d snapshots, want 1", count)
	}
	return system
}

func TestTimeSnapshotFreshAcrossTurns(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	dates := []time.Time{
		time.Date(2026, 11, 10, 23, 59, 59, 0, location),
		time.Date(2026, 11, 11, 0, 0, 1, 0, location),
	}
	for _, tools := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("tools=%t/stream=%t", tools, streaming), func(t *testing.T) {
				var turn, requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					req := readRequest(t, r)
					requests.Add(1)
					index := int(turn.Load())
					system := assertTimeSnapshotRequest(t, req, timeSnapshotPrompt(dates[index]))
					if index > 0 && strings.Contains(system, dates[0].Format(time.RFC3339)) {
						t.Error("previous turn's time leaked into fresh system prompt")
					}
					if req["stream"] == true {
						sseChunk(w, map[string]any{"role": "assistant", "content": "收到。"}, nil)
						sseEnd(w)
					} else if tools && streaming {
						// Fake selection demonstrates READY proceeds to final speech;
						// this does not test a real model's tool-selection judgment.
						replyJSON(w, "READY")
					} else {
						replyJSON(w, "收到。")
					}
				}))
				defer server.Close()
				engine, err := New(context.Background(), Config{BaseURL: server.URL + "/v1", Model: "test", Persona: "测试助手", Tools: tools}, &testHost{})
				if err != nil {
					t.Fatal(err)
				}
				defer engine.Close()
				if engine.timeNow == nil {
					t.Fatal("constructor did not initialize timeNow")
				}
				for i, now := range dates {
					turn.Store(int32(i))
					var clockCalls atomic.Int32
					engine.timeNow = func() time.Time {
						// Any accidental resampling produces a visibly different date.
						return now.Add(time.Duration(clockCalls.Add(1)-1) * 48 * time.Hour)
					}
					var answer, deltas string
					if streaming {
						answer, err = engine.ReplyStream(context.Background(), "今天几号，星期几，现在几点？", func(delta string) error { deltas += delta; return nil })
					} else {
						answer, err = engine.Reply(context.Background(), "今天几号，星期几，现在几点？")
					}
					if err != nil || answer != "收到。" || (streaming && deltas != answer) {
						t.Fatalf("turn %d answer=%q deltas=%q err=%v", i, answer, deltas, err)
					}
					if clockCalls.Load() != 1 {
						t.Errorf("turn %d sampled time %d times, want 1", i, clockCalls.Load())
					}
					assertTimeSnapshotTransient(t, engine, timeSnapshotPrompt(now), i+1)
				}
				wantRequests := int32(2)
				if tools && streaming {
					wantRequests = 4 // READY selection and final stream for each turn.
				}
				if requests.Load() != wantRequests {
					t.Errorf("requests=%d want=%d", requests.Load(), wantRequests)
				}
			})
		}
	}
}

func TestTimeSnapshotStableThroughToolContinuation(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			now := time.Date(2026, 11, 11, 0, 0, 1, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
			snapshot := timeSnapshotPrompt(now)
			var requests, clockCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				assertTimeSnapshotRequest(t, req, snapshot)
				n := requests.Add(1)
				if n == 1 {
					callsJSON(w, []any{call("metrics", "get_metrics", "{}")})
					return
				}
				found := false
				for _, raw := range req["messages"].([]any) {
					message := raw.(map[string]any)
					if message["role"] == "tool" && message["tool_call_id"] == "metrics" {
						found = true
					}
				}
				if !found {
					t.Error("metrics result missing from continuation")
				}
				if req["stream"] == true {
					sseChunk(w, map[string]any{"role": "assistant", "content": "CPU 使用率为百分之十二。"}, nil)
					sseEnd(w)
				} else if streaming {
					replyJSON(w, "READY")
				} else {
					replyJSON(w, "CPU 使用率为百分之十二。")
				}
			}))
			defer server.Close()
			memory := OpenMemory("")
			engine, err := New(context.Background(), Config{BaseURL: server.URL + "/v1", Model: "test", Persona: "测试助手", Tools: true, Memory: memory}, &testHost{})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			engine.timeNow = func() time.Time {
				return now.Add(time.Duration(clockCalls.Add(1)-1) * 48 * time.Hour)
			}
			var answer, deltas string
			if streaming {
				answer, err = engine.ReplyStream(context.Background(), "现在几点，另外查看 CPU 使用率。", func(delta string) error { deltas += delta; return nil })
			} else {
				answer, err = engine.Reply(context.Background(), "现在几点，另外查看 CPU 使用率。")
			}
			if err != nil || answer != "CPU 使用率为百分之十二。" || (streaming && deltas != answer) {
				t.Fatalf("answer=%q deltas=%q err=%v", answer, deltas, err)
			}
			wantRequests := int32(2)
			if streaming {
				wantRequests = 3
			}
			if requests.Load() != wantRequests || clockCalls.Load() != 1 {
				t.Errorf("requests=%d want=%d clock samples=%d want=1", requests.Load(), wantRequests, clockCalls.Load())
			}
			assertTimeSnapshotTransient(t, engine, snapshot, 1)
			turns := memory.Turns()
			if len(turns) != 1 || turns[0].User != "现在几点，另外查看 CPU 使用率。" || turns[0].Assistant != answer {
				t.Fatalf("transient snapshot or tool messages entered memory: %+v", turns)
			}
		})
	}
}

func assertTimeSnapshotTransient(t *testing.T, engine *Engine, snapshot string, turns int) {
	t.Helper()
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if strings.Count(engine.contextPrompt, "本轮时间快照：") != 1 || !strings.Contains(engine.contextPrompt, snapshot) {
		t.Errorf("context accounting lost current snapshot: %q", engine.contextPrompt)
	}
	wantTokens := estimateMessages(engine.messagesLocked(engine.persona+engine.contextPrompt, "")) + engine.toolTokens
	withoutSnapshot := estimateMessages(engine.messagesLocked(engine.persona, "")) + engine.toolTokens
	if engine.stats.EstimatedTokens != wantTokens || wantTokens <= withoutSnapshot {
		t.Errorf("snapshot not counted in context stats: got=%d want=%d without=%d", engine.stats.EstimatedTokens, wantTokens, withoutSnapshot)
	}
	if len(engine.history) != 2*turns {
		t.Errorf("history contains %d messages, want %d", len(engine.history), 2*turns)
	}
	for i, message := range engine.history {
		wantRole := "user"
		if i%2 == 1 {
			wantRole = "assistant"
		}
		if string(message.Role) != wantRole || len(message.ToolCalls) != 0 || message.ToolCallID != "" || strings.Contains(message.Content, "本轮时间快照") {
			t.Errorf("transient context leaked into history[%d]: %+v", i, message)
		}
	}
	if strings.Contains(engine.summary, "本轮时间快照") {
		t.Error("transient snapshot entered conversation summary")
	}
}
