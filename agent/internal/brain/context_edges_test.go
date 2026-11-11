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

	"aemeath/agent/internal/hamcp"
	"github.com/cloudwego/eino/schema"
)

// Read one usage event without EOF; accounting remains pending until Close.
func pendingEdgeUsage(t *testing.T, e *Engine, prompt, completion int) *usageStreamBody {
	t.Helper()
	e.mu.Lock()
	scope := requestScope{generation: e.generation, epoch: e.memoryEpoch}
	e.mu.Unlock()
	event := fmt.Sprintf("data: {\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", prompt, completion, prompt+completion)
	body := &usageStreamBody{ReadCloser: io.NopCloser(strings.NewReader(event)), engine: e, scope: scope}
	buf := make([]byte, len(event))
	if n, err := body.Read(buf); n != len(event) || err != nil {
		t.Fatalf("read usage before EOF: n=%d err=%v", n, err)
	}
	t.Cleanup(func() { body.Close() })
	return body
}

func TestContextEdgesUsageSurvivesReconnectAndCloseButNotClear(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "回答"}}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11}})
	}))
	defer s.Close()
	m := OpenMemory("")
	first := contextEngine(t, s, m, false, nil)
	pending := pendingEdgeUsage(t, first, 100, 10)
	second := contextEngine(t, s, m, false, nil)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	pending.Close()
	pending.Close()
	if _, err := second.Reply(context.Background(), "新连接问题"); err != nil {
		t.Fatal(err)
	}
	for name, stats := range map[string]ContextStats{"engine": second.ContextStats(), "memory": m.ContextStats()} {
		if !stats.UsageAvailable || stats.PromptTokens != 110 || stats.CompletionTokens != 11 || stats.TotalTokens != 121 {
			t.Fatalf("%s lost or duplicated late usage: %+v", name, stats)
		}
	}
	stale := pendingEdgeUsage(t, second, 90, 9)
	second.Clear()
	stale.Close()
	stale.Close()
	if stats := m.ContextStats(); stats.UsageAvailable || stats.TotalTokens != 0 {
		t.Fatalf("cleared usage resurrected: %+v", stats)
	}
	if _, err := second.Reply(context.Background(), "清理后问题"); err != nil {
		t.Fatal(err)
	}
	if stats := second.ContextStats(); stats.TotalTokens != 11 || stats.PromptTokens != 10 || stats.CompletionTokens != 1 {
		t.Fatalf("new epoch usage contaminated: %+v", stats)
	}
}

func TestContextEdgesUsageAfterCloseWithoutSharedMemory(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected model request") }))
	defer s.Close()
	e := contextEngine(t, s, nil, false, nil)
	body := pendingEdgeUsage(t, e, 7, 2)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	body.Close()
	body.Close()
	if stats := e.ContextStats(); stats.TotalTokens != 9 || stats.PromptTokens != 7 || stats.CompletionTokens != 2 {
		t.Fatalf("Close erased observed usage: %+v", stats)
	}
}

func TestContextEdgesRejectedToolFanoutStillCountsUsage(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		requests.Add(1)
		calls := make([]any, 9)
		for i := range calls {
			calls[i] = call(fmt.Sprint(i), "get_metrics", "{}")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": calls}}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}})
	}))
	defer s.Close()
	host := &largeMetricsHost{}
	e := contextEngine(t, s, OpenMemory(""), true, host)
	_, err := e.Reply(context.Background(), "查看状态")
	var failure *requestFailure
	if !errors.As(err, &failure) || failure.code != "tool_call_limit" {
		t.Fatalf("fanout error=%v", err)
	}
	if requests.Load() != 1 || host.calls.Load() != 0 {
		t.Fatalf("fanout replayed or executed: requests=%d calls=%d", requests.Load(), host.calls.Load())
	}
	if stats := e.ContextStats(); stats.TotalTokens != 120 || stats.PromptTokens != 100 || stats.CompletionTokens != 20 {
		t.Fatalf("rejected response usage lost: %+v", stats)
	}
	if turns := e.memory.Turns(); len(turns) != 0 {
		t.Fatalf("rejected response committed %d turns", len(turns))
	}
}

func TestContextEdgesFirstLargeToolSchemaTrimsBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		requests.Add(1)
		if isSummaryRequest(req) {
			t.Error("four retained turns should be trimmed directly")
		}
		if lenOrZero(req["tools"]) != 1 {
			t.Errorf("large tool schema missing: %v", req["tools"])
		}
		replyJSON(w, "好的")
	}))
	defer s.Close()
	e := contextEngine(t, s, nil, false, nil)
	e.mu.Lock()
	for i := 0; i < 4; i++ {
		e.history = append(e.history, schema.UserMessage(strings.Repeat("a", 1500)), schema.AssistantMessage(strings.Repeat("b", 1500), nil))
	}
	e.lastActive = time.Now()
	e.updateContextLocked()
	original := estimateMessages(e.messagesLocked(e.persona, "继续"))
	e.mu.Unlock()
	params, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string", "description": strings.Repeat("x", 7500)}}})
	tools := makeHomeAssistantTools(&fakeHA{}, []hamcp.Tool{{Name: "LargeTool", Description: "测试大参数定义", InputSchema: params}}, "")
	if len(tools) != 1 {
		t.Fatal("large schema fixture rejected")
	}
	if err := e.rebuildAgent(context.Background(), tools); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	toolTokens, hardLimit := e.toolTokens, e.stats.InputBudget+e.stats.ContextWindow/10
	e.mu.Unlock()
	// Without preflight, this initial request exceeds the hard transport limit.
	if toolTokens >= e.ContextStats().InputBudget || original+toolTokens <= hardLimit {
		t.Fatalf("fixture does not cross hard limit: messages=%d tools=%d hard=%d", original, toolTokens, hardLimit)
	}
	if _, err := e.Reply(context.Background(), "继续"); err != nil {
		t.Fatalf("first request should fit after trimming: %v", err)
	}
	if stats := e.ContextStats(); requests.Load() != 1 || stats.DroppedTurns == 0 || stats.RetainedTurns > 4 {
		t.Fatalf("first request did not trim before sending: requests=%d stats=%+v", requests.Load(), stats)
	}
}

func TestContextEdgesSummaryWrapperExpansionRejected(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSummaryRequest(readRequest(t, r)) {
			t.Error("unexpected ordinary request")
		}
		replyJSON(w, "记忆")
	}))
	defer s.Close()
	e := contextEngine(t, s, nil, false, nil)
	e.mu.Lock()
	for i := 0; i < 5; i++ {
		e.history = append(e.history, schema.UserMessage("u"), schema.AssistantMessage("a", nil))
	}
	e.lastActive = time.Now()
	e.updateContextLocked()
	before := e.stats.EstimatedTokens
	e.mu.Unlock()
	awaitCompression(t, e.startCompression(context.Background()))
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.summary != "" || e.stats.Compressions != 0 || e.stats.DroppedTurns != 0 || e.stats.RetainedTurns != 5 || e.stats.LastCompression != "failed" || e.stats.EstimatedTokens != before {
		t.Fatalf("expanding summary accepted or history changed: summary=%q before=%d stats=%+v", e.summary, before, e.stats)
	}
}

func TestContextEdgesCompressionTimeoutFallsBackButCallerCancelDoesNot(t *testing.T) {
	for _, mode := range []string{"internal_timeout", "caller_cancel"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !isSummaryRequest(readRequest(t, r)) {
					t.Error("unexpected ordinary request")
				}
				close(entered)
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			}))
			defer s.Close()
			e := contextEngine(t, s, nil, false, nil)
			seedContext(e, 12)
			if mode == "internal_timeout" {
				e.compressionTimeout = 20 * time.Millisecond
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := e.startCompression(parent)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("summary did not reach server")
			}
			if mode == "caller_cancel" {
				cancel()
			}
			awaitCompression(t, done)
			stats := e.ContextStats()
			if stats.Compressing {
				t.Fatalf("finished compression still active: %+v", stats)
			}
			if mode == "internal_timeout" {
				if stats.LastCompression != "fallback" || stats.DroppedTurns == 0 || stats.RetainedTurns >= 12 || stats.Compressions != 0 {
					t.Fatalf("internal timeout skipped fallback: %+v", stats)
				}
			} else if stats.DroppedTurns != 0 || stats.RetainedTurns != 12 || stats.Compressions != 0 {
				t.Fatalf("caller cancellation trimmed history: %+v", stats)
			}
		})
	}
}
