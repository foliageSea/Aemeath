package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLocalUsageWithoutProviderUsage(t *testing.T) {
	var input atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		input.Add(int64(3 + estimateJSON(req["messages"]) + estimateJSON(req["tools"])))
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, text := range []string{"hel", "lo wo", "rld", "中", "文", "!"} {
				data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}})
				fmt.Fprintf(w, "data: %s\n\n", data)
			}
			sseEnd(w)
		} else {
			replyJSON(w, "hello world中文!")
		}
	}))
	defer server.Close()
	e := contextEngine(t, server, nil, false, nil)
	if _, err := e.Reply(context.Background(), "第一问"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReplyStream(context.Background(), "继续", nil); err != nil {
		t.Fatal(err)
	}
	stats := e.ContextStats()
	if stats.UsageAvailable {
		t.Fatal("fixture unexpectedly reported provider usage")
	}
	if stats.LocalPromptTokens != int(input.Load()) || stats.LocalCompletionTokens != 18 || stats.LocalTotalTokens != int(input.Load())+18 {
		t.Fatalf("local usage missing or inflated by stream chunks: %+v, input=%d", stats, input.Load())
	}
	e.Clear()
	if stats = e.ContextStats(); stats.LocalTotalTokens != 0 {
		t.Fatalf("clear retained local usage: %+v", stats)
	}
}

func TestLocalOutputCountsReasoningAndFragmentedToolsOnce(t *testing.T) {
	var full, stream localOutputCounter
	encode := func(value any) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	full.observe(encode(map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{
		"role": "assistant", "content": "hello world中文!", "reasoning_content": "think first",
		"tool_calls": []any{call("call-1", "get_metrics", `{"key":"value"}`)},
	}}}}))
	delta := func(content, reasoning, name, args string) string {
		message := map[string]any{"content": content, "reasoning_content": reasoning}
		if name != "" || args != "" {
			message["tool_calls"] = []any{map[string]any{"index": 0, "function": map[string]any{"name": name, "arguments": args}}}
		}
		return string(encode(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": message}}}))
	}
	chunks := []string{
		delta("hello ", "thi", "", ""),
		delta("world中文!", "nk first", "get_", `{"key":`),
		delta("", "", "metrics", `"value"}`),
		`{"choices":[],"usage":{"prompt_tokens":100000,"completion_tokens":99999,"total_tokens":199999}}`,
	}
	for _, chunk := range chunks {
		stream.observe([]byte(chunk))
	}
	want := estimateTokens("hello world中文!") + estimateTokens("think first") + estimateTokens("get_metrics") + estimateTokens(`{"key":"value"}`)
	if full.tokens() != want || stream.tokens() != want {
		t.Fatalf("JSON=%d SSE=%d want=%d", full.tokens(), stream.tokens(), want)
	}
	e := &Engine{}
	body := &usageStreamBody{ReadCloser: io.NopCloser(strings.NewReader("data: " + chunks[0] + "\n\ndata: " + chunks[1] + "\n\ndata: " + chunks[2] + "\n\n")), engine: e}
	buf := make([]byte, 1)
	for {
		_, err := body.Read(buf)
		if err != nil {
			break
		}
	}
	body.Close()
	body.Close()
	if got := e.ContextStats().LocalCompletionTokens; got != want {
		t.Fatalf("fragmented SSE or repeated Close counted %d want %d", got, want)
	}
}

func TestLocalUsageSurvivesReconnectButNotClear(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") }))
	defer server.Close()
	memory := OpenMemory("")
	first := contextEngine(t, server, memory, false, nil)
	scope := requestScope{generation: first.generation, epoch: first.memoryEpoch}
	first.addLocalUsage(100, 10, scope)
	second := contextEngine(t, server, memory, false, nil)
	first.Close()
	first.addLocalUsage(0, 5, scope)
	for name, stats := range map[string]ContextStats{"engine": second.ContextStats(), "memory": memory.ContextStats()} {
		if stats.LocalPromptTokens != 100 || stats.LocalCompletionTokens != 15 || stats.LocalTotalTokens != 115 {
			t.Fatalf("%s lost local usage: %+v", name, stats)
		}
	}
	second.Clear()
	first.addLocalUsage(1, 1, scope)
	if stats := memory.ContextStats(); stats.LocalTotalTokens != 0 {
		t.Fatalf("stale callback resurrected local usage: %+v", stats)
	}
	second.addLocalUsage(20, 2, requestScope{generation: second.generation, epoch: second.memoryEpoch})
	if stats := second.ContextStats(); stats.LocalTotalTokens != 22 {
		t.Fatalf("new epoch contaminated: %+v", stats)
	}
}

func TestLocalUsageIncludesSummaryAndToolRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		callsJSON(w, []any{call("tool-1", "get_metrics", "{}")})
	}))
	defer server.Close()
	e := contextEngine(t, server, nil, false, nil)
	for _, summary := range []bool{false, true} {
		payload := `{"messages":[{"role":"user","content":"问题"}],"tools":[{"type":"function","function":{"name":"get_metrics","description":"系统指标"}}]}`
		ctx := context.WithValue(context.Background(), requestScopeKey{}, requestScope{generation: e.generation, summary: summary})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		res, err := (boundedTransport{engine: e}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	stats := e.ContextStats()
	if stats.LocalPromptTokens <= 0 || stats.LocalCompletionTokens != 2*(estimateTokens("get_metrics")+estimateTokens("{}")) || stats.UsageAvailable {
		t.Fatalf("tool or summary usage omitted: %+v", stats)
	}
	// A rejected budget never reaches the server or changes cumulative usage.
	data, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": strings.Repeat("中", 50000)}}})
	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (boundedTransport{engine: e}).RoundTrip(req); err == nil {
		t.Fatal("oversized request accepted")
	}
	if got := e.ContextStats().LocalTotalTokens; got != stats.LocalTotalTokens {
		t.Fatalf("unsent request counted: %d want %d", got, stats.LocalTotalTokens)
	}
}
