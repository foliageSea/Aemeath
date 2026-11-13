package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func contextEngine(t *testing.T, s *httptest.Server, m *Memory, tools bool, host Host) *Engine {
	t.Helper()
	e, err := New(context.Background(), Config{BaseURL: s.URL + "/v1", Model: "test", Persona: "测试助手", ContextWindow: 8192, Memory: m, Tools: tools}, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}
func seedContext(e *Engine, turns int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := 0; i < turns; i++ {
		e.history = append(e.history, schema.UserMessage(fmt.Sprintf("用户偏好%d ", i)+strings.Repeat("a", 590)), schema.AssistantMessage(strings.Repeat("b", 600), nil))
	}
	e.lastActive = time.Now()
	e.updateContextLocked()
	e.saveMemoryLocked()
}
func isSummaryRequest(req map[string]any) bool {
	messages, _ := req["messages"].([]any)
	return len(messages) > 0 && messages[0].(map[string]any)["content"] == summaryInstruction
}
func awaitCompression(t *testing.T, done <-chan struct{}) {
	t.Helper()
	if done == nil {
		t.Fatal("compression not started")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("compression did not finish")
	}
}

func TestContextRollingSummaryAndRestart(t *testing.T) {
	var summaries, normal atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		if isSummaryRequest(req) {
			summaries.Add(1)
			if lenOrZero(req["tools"]) != 0 || req["tool_choice"] != "none" {
				t.Error("summarizer must not execute tools")
			}
			if !strings.Contains(req["messages"].([]any)[1].(map[string]any)["content"].(string), "用户偏好0") {
				t.Error("old facts not passed to summarizer")
			}
			replyJSON(w, "用户希望回答简短；正在讨论设备。")
			return
		}
		normal.Add(1)
		msgs := req["messages"].([]any)
		if len(msgs) != 11 || msgs[1].(map[string]any)["role"] != "assistant" || !strings.Contains(msgs[1].(map[string]any)["content"].(string), "回答简短") {
			t.Errorf("summary / recent four turns missing: messages=%d", len(msgs))
		}
		replyJSON(w, "好的")
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "memory.json")
	e := contextEngine(t, s, OpenMemory(path), false, nil)
	seedContext(e, 12)
	if _, err := e.Reply(context.Background(), "继续"); err != nil {
		t.Fatal(err)
	}
	stats := e.ContextStats()
	if summaries.Load() != 1 || normal.Load() != 1 || stats.Compressions != 1 || stats.RetainedTurns != 5 || stats.EstimatedTokens >= stats.InputBudget {
		t.Fatalf("unexpected compression stats %+v", stats)
	}
	restored := OpenMemory(path)
	if restored.summary == "" || len(restored.Turns()) != 5 || restored.ContextStats().SummaryTokens == 0 {
		t.Fatal("summary not restored")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("memory permissions", info, err)
	}
}

func TestContextIdleHistoryStillCompressesAndContinues(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared=%v", shared), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				if isSummaryRequest(req) {
					replyJSON(w, "用户喜欢简短回答。")
					return
				}
				messages := req["messages"].([]any)
				if len(messages) != 11 || !strings.Contains(messages[1].(map[string]any)["content"].(string), "用户喜欢简短回答") {
					t.Errorf("idle compressed context not sent: %v", messages)
				}
				replyJSON(w, "继续回答")
			}))
			defer s.Close()
			var memory *Memory
			path := filepath.Join(t.TempDir(), "memory.json")
			if shared {
				memory = OpenMemory(path)
			}
			e := contextEngine(t, s, memory, false, nil)
			seedContext(e, 10)
			e.mu.Lock()
			e.lastActive = time.Now().Add(-7 * 24 * time.Hour)
			e.saveMemoryLocked()
			e.mu.Unlock()
			if stats := e.ContextStats(); stats.RetainedTurns != 10 || stats.EstimatedTokens <= 0 {
				t.Fatalf("idle context disappeared before compression: %+v", stats)
			}
			awaitCompression(t, e.startCompression(e.lifeCtx))
			if stats := e.ContextStats(); stats.RetainedTurns != 4 || stats.Compressions != 1 || stats.SummaryTokens <= 0 || stats.Compressing {
				t.Fatalf("idle context was not compressed: %+v", stats)
			}
			if shared {
				restored := OpenMemory(path)
				if stats := restored.ContextStats(); len(restored.Turns()) != 4 || stats.SummaryTokens <= 0 || stats.Compressions != 1 {
					t.Fatalf("idle compressed context not persisted: %+v", stats)
				}
			}
			if _, err := e.Reply(context.Background(), "继续"); err != nil {
				t.Fatal(err)
			}
			if stats := e.ContextStats(); stats.RetainedTurns != 5 || stats.Compressions != 1 || stats.SummaryTokens <= 0 {
				t.Fatalf("idle compressed context lost on continuation: %+v", stats)
			}
		})
	}
}

func TestContextBackgroundSummaryKeepsAppendedTurn(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSummaryRequest(readRequest(t, r)) {
			close(entered)
			select {
			case <-release:
				replyJSON(w, "保留用户偏好。")
			case <-r.Context().Done():
			}
			return
		}
		replyJSON(w, "新回答")
	}))
	defer s.Close()
	e := contextEngine(t, s, OpenMemory(""), false, nil)
	seedContext(e, 10)
	e.mu.Lock()
	// Keep this scenario in the background-only range even when the per-turn
	// system context grows: Reply must not wait for the blocked summarizer.
	e.contextPrompt = timeSnapshotPrompt(e.timeNow())
	requestTokens := estimateMessages(e.messagesLocked(e.persona+e.contextPrompt, "新消息")) + e.toolTokens
	e.stats.InputBudget = requestTokens*100/80 + 1
	e.updateContextLocked()
	e.mu.Unlock()
	e.maybeCompress()
	<-entered
	e.mu.Lock()
	done := e.compDone
	e.mu.Unlock()
	if !e.ContextStats().Compressing {
		t.Fatal("background status missing")
	}
	if _, err := e.Reply(context.Background(), "新消息"); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitCompression(t, done)
	stats := e.ContextStats()
	if stats.RetainedTurns != 5 || stats.Compressions != 1 || stats.Compressing {
		t.Fatalf("appended turn lost %+v", stats)
	}
	e.mu.Lock()
	last := e.history[len(e.history)-2].Content
	e.mu.Unlock()
	if last != "新消息" {
		t.Fatal("new turn overwritten")
	}
}

func TestContextFailedSummaryFallsBackWithoutFailingReply(t *testing.T) {
	var summaries atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSummaryRequest(readRequest(t, r)) {
			summaries.Add(1)
			w.WriteHeader(503)
			return
		}
		replyJSON(w, "仍能回答")
	}))
	defer s.Close()
	e := contextEngine(t, s, nil, false, nil)
	seedContext(e, 12)
	if answer, err := e.Reply(context.Background(), "继续"); err != nil || answer != "仍能回答" {
		t.Fatal(answer, err)
	}
	stats := e.ContextStats()
	if summaries.Load() != 1 || stats.LastCompression != "fallback" || stats.DroppedTurns == 0 || stats.Compressions != 0 || stats.EstimatedTokens > stats.InputBudget {
		t.Fatalf("fallback stats %+v", stats)
	}
}

func TestContextClearAndReconnectInvalidateBackground(t *testing.T) {
	for _, mode := range []string{"clear", "external_clear", "reconnect", "close"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if isSummaryRequest(readRequest(t, r)) {
					close(entered)
					select {
					case <-release:
						replyJSON(w, "过期摘要")
					case <-r.Context().Done():
					}
					return
				}
				replyJSON(w, "新会话回答")
			}))
			defer s.Close()
			path := filepath.Join(t.TempDir(), "memory.json")
			m := OpenMemory(path)
			e := contextEngine(t, s, m, false, nil)
			seedContext(e, 11)
			done := e.startCompression(e.lifeCtx)
			<-entered
			var next *Engine
			switch mode {
			case "clear":
				e.Clear()
			case "external_clear":
				m.Clear()
			case "reconnect":
				next = contextEngine(t, s, m, false, nil)
			case "close":
				e.Close()
			}
			close(release)
			awaitCompression(t, done)
			if mode == "clear" || mode == "external_clear" {
				if len(m.Turns()) != 0 || m.summary != "" || m.ContextStats().Compressing {
					t.Fatal("clear resurrected by old summary")
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("old summary recreated file", err)
				}
				if _, err := e.Reply(context.Background(), "清空后的新问题"); err != nil {
					t.Fatal(err)
				}
				if len(m.Turns()) != 1 || m.Turns()[0].User != "清空后的新问题" {
					t.Fatal("old history reused after clear")
				}
			}
			if mode == "reconnect" {
				if m.summary != "" {
					t.Fatal("superseded connection persisted summary")
				}
				// Avoid triggering a second summary in this test; replace the loaded history
				// under the new owner's lock, then exercise a real persisted reply.
				next.mu.Lock()
				next.history = nil
				next.updateContextLocked()
				next.mu.Unlock()
				if _, err := next.Reply(context.Background(), "重连后的问题"); err != nil {
					t.Fatal(err)
				}
				if len(m.Turns()) != 1 || m.Turns()[0].User != "重连后的问题" {
					t.Fatal("new connection lost memory ownership")
				}
			}
			if mode == "close" && (e.ContextStats().Compressing || m.summary != "") {
				t.Fatal("close left stale background state")
			}
		})
	}
}

func TestContextBudgetRejectsOversizedCurrentInput(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); replyJSON(w, "unexpected") }))
	defer s.Close()
	e := contextEngine(t, s, nil, false, nil)
	_, err := e.Reply(context.Background(), strings.Repeat("中", 4000))
	var failure *requestFailure
	if !errors.As(err, &failure) || failure.code != "context_limit" || requests.Load() != 0 {
		t.Fatalf("oversized request sent: %v %d", err, requests.Load())
	}
}

func TestContextBudgetChecksLiveToolResultsWithoutReplay(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		requests.Add(1)
		callsJSON(w, []any{call("m", "get_metrics", "{}")})
	}))
	defer s.Close()
	host := &largeMetricsHost{}
	e := contextEngine(t, s, nil, true, host)
	e.authorizeTool = allowToolAuthorizationForTest // isolate live-result budgeting
	_, err := e.Reply(context.Background(), "查看状态")
	var failure *requestFailure
	if !errors.As(err, &failure) || failure.code != "context_limit" || requests.Load() != 1 || host.calls.Load() != 1 {
		t.Fatalf("tool request replayed or budget ignored: %v requests=%d tools=%d", err, requests.Load(), host.calls.Load())
	}
	if len(e.history) != 0 {
		t.Fatal("failed tool turn committed")
	}
}

type largeMetricsHost struct {
	testHost
	calls atomic.Int32
}

func (h *largeMetricsHost) Metrics() string { h.calls.Add(1); return strings.Repeat("中", 5000) }

func TestContextUsageCountsJSONAndSSEOnce(t *testing.T) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		n.Add(1)
		usage := map[string]int{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}
		if req["stream"] == true {
			// Repeat cumulative usage and split it across individual reads downstream.
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < 2; i++ {
				data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "答"}, "finish_reason": nil}}, "usage": usage})
				fmt.Fprintf(w, "data: %s\n\n", data)
			}
			sseEnd(w)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "回答"}}}, "usage": usage})
	}))
	defer s.Close()
	e := contextEngine(t, s, nil, false, nil)
	if _, err := e.Reply(context.Background(), "问题"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReplyStream(context.Background(), "继续", nil); err != nil {
		t.Fatal(err)
	}
	stats := e.ContextStats()
	if !stats.UsageAvailable || stats.PromptTokens != 200 || stats.CompletionTokens != 40 || stats.TotalTokens != 240 || n.Load() != 2 {
		t.Fatalf("usage double-counted / lost %+v", stats)
	}
	// Bytes split in the middle of a JSON token must not break observation.
	body := &usageStreamBody{ReadCloser: io.NopCloser(strings.NewReader("data: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9}}\n\n")), engine: e}
	buf := make([]byte, 1)
	for {
		_, err := body.Read(buf)
		if err != nil {
			break
		}
	}
	body.Close()
	body.Close()
	if e.ContextStats().TotalTokens != 249 {
		t.Fatal("fragmented SSE usage lost or counted twice")
	}
	e.Clear()
	if e.ContextStats().UsageAvailable || e.ContextStats().TotalTokens != 0 {
		t.Fatal("clear retained old usage")
	}
}

func TestContextSummaryAndLegacyMemorySurviveInactivity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	legacy := memoryFile{Version: 1, UpdatedAt: time.Now().Add(-7 * 24 * time.Hour), Turns: []memoryTurn{{User: "旧偏好", Assistant: "已记录"}}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if m := OpenMemory(path); len(m.Turns()) != 1 || m.ContextStats().RetainedTurns != 1 {
		t.Fatal("idle legacy memory not read")
	}
	idle := memoryFile{Version: 2, UpdatedAt: time.Now().Add(-7 * 24 * time.Hour), Summary: "用户喜欢清淡的食物"}
	data, err = json.Marshal(idle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		messages := req["messages"].([]any)
		if len(messages) != 3 || messages[1].(map[string]any)["role"] != "assistant" || !strings.Contains(messages[1].(map[string]any)["content"].(string), idle.Summary) {
			t.Errorf("idle summary not sent: %v", messages)
		}
		replyJSON(w, "继续之前的对话")
	}))
	defer s.Close()
	m := OpenMemory(path)
	e := contextEngine(t, s, m, false, nil)
	if m.ContextStats().SummaryTokens == 0 || e.ContextStats().SummaryTokens == 0 {
		t.Fatal("idle summary not shown")
	}
	if _, err := e.Reply(context.Background(), "今天吃什么"); err != nil {
		t.Fatal(err)
	}
	if saved := OpenMemory(path); saved.summary != idle.Summary || len(saved.Turns()) != 1 {
		t.Fatal("idle summary or continued turn not persisted")
	}
	e.Clear()
	if saved := OpenMemory(path); saved.summary != "" || len(saved.Turns()) != 0 {
		t.Fatal("manual clear retained idle summary or history")
	}
}
