package brain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// These are response BODY strings, not structured tool_calls. A provider may
// ignore tool_choice=none and print its private tool protocol as ordinary text.
var protocolLeakBodies = []struct {
	name string
	body string
}{
	{"dsml_unicode", `<｜DSML｜function_calls><｜DSML｜invoke name="web_search"><｜DSML｜parameter name="query" string="true">protocol-secret-query</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜function_calls>`},
	{"dsml_ascii", `<|DSML|function_calls><|DSML|invoke name="web_search"><|DSML|parameter name="query" string="true">protocol-secret-query</|DSML|parameter></|DSML|invoke></|DSML|function_calls>`},
	{"dsml_mixed_left", `<｜DSML|function_calls><｜DSML|invoke name="set_page">{"page":"quota","private":"protocol-secret-query"}</｜DSML|invoke></｜DSML|function_calls>`},
	{"dsml_mixed_right", `<|DSML｜function_calls><|DSML｜invoke name="set_page">{"page":"quota","private":"protocol-secret-query"}</|DSML｜invoke></|DSML｜function_calls>`},
	{"dsml_double_unicode", `<｜｜DSML｜function_calls><｜｜DSML｜invoke name="web_search">protocol-secret-query</｜｜DSML｜invoke></｜｜DSML｜function_calls>`},
	{"dsml_double_ascii", `<||DSML|function_calls><||DSML|invoke name="web_search">protocol-secret-query</||DSML|invoke></||DSML|function_calls>`},
	{"dsml_closing_uppercase", `</||dsml|function_calls>protocol-secret-query`},
	{"closing_uppercase", `</TOOL_CALL>protocol-secret-query`},
	{"tool_search", `<tool_search>{"query":"protocol-secret-query"}</tool_search>`},
	{"tool_call", `<tool_call>{"name":"set_page","arguments":{"page":"quota","private":"protocol-secret-query"}}</tool_call>`},
	{"function_calls", `<function_calls><invoke name="web_search"><parameter name="query">protocol-secret-query</parameter></invoke></function_calls>`},
}

// Reuse the real HTTP/Eino and tool helpers from brain_test.go/stream_test.go.
// With tools enabled, set_page stands in for already completed device/search
// work: a failed final answer must never restart that work or retry the graph.
func newProtocolLeakFixture(t *testing.T, streaming, tools, selectorLeak bool, chunks []string) (*Engine, *testHost, *atomic.Int32) {
	t.Helper()
	requests := new(atomic.Int32)
	host := &testHost{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		n := requests.Add(1)
		if tools && n == 1 {
			if req["stream"] == true || lenOrZero(req["tools"]) == 0 {
				t.Error("initial tool selection must be a completed, tool-enabled request")
			}
			callsJSON(w, []any{call("protocol-page", "set_page", `{"page":"voice"}`)})
			return
		}
		if tools && streaming && n == 2 && !selectorLeak {
			if req["stream"] == true {
				t.Error("tool selector unexpectedly streamed")
			}
			replyJSON(w, "READY")
			return
		}
		if streaming && !selectorLeak {
			if req["stream"] != true || lenOrZero(req["tools"]) != 0 || req["tool_choice"] != "none" {
				t.Errorf("final stream must forbid tools: %v", req)
			}
			sseChunk(w, map[string]any{"role": "assistant"}, nil)
			for _, chunk := range chunks {
				sseChunk(w, map[string]any{"content": chunk}, nil)
			}
			sseEnd(w)
			return
		}
		if req["stream"] == true {
			t.Error("completed reply/selector unexpectedly streamed")
		}
		replyJSON(w, strings.Join(chunks, ""))
	}))
	t.Cleanup(s.Close)
	e := newEngine(t, s, tools, host)
	t.Cleanup(func() { _ = e.Close() })
	// Existing successful history must survive without committing the failed turn.
	e.history = []*schema.Message{schema.UserMessage("旧问题"), schema.AssistantMessage("旧答案。", nil)}
	return e, host, requests
}

func assertProtocolLeakRejected(t *testing.T, e *Engine, host *testHost, requests *atomic.Int32, text, spoken, safePrefix string, err error, wantRequests, wantWrites int32) {
	t.Helper()
	var failure *requestFailure
	if !errors.As(err, &failure) || failure.code != "tool_protocol" || failure.status != 0 {
		t.Errorf("error=%v (%+v), want requestFailure code tool_protocol", err, failure)
	}
	if err != nil && (errors.Unwrap(err) != nil || strings.Contains(err.Error(), "protocol-secret-query")) {
		t.Errorf("public error retained private protocol: %v", err)
	}
	if text != "" {
		t.Errorf("failed response escaped as return text: %q", text)
	}
	// Even an incomplete marker prefix such as < or <｜DS must be held back.
	// Safe prose already sent before the marker need not be recalled.
	if !strings.HasPrefix(safePrefix, spoken) {
		t.Errorf("marker or arguments escaped to onDelta: %q (safe prefix %q)", spoken, safePrefix)
	}
	if len(e.history) != 2 || e.history[0].Content != "旧问题" || e.history[1].Content != "旧答案。" {
		t.Errorf("failed turn changed durable history: %+v", e.history)
	}
	if requests.Load() != wantRequests {
		t.Errorf("requests=%d, want %d; protocol failures must not retry", requests.Load(), wantRequests)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.writes != int(wantWrites) || (wantWrites == 1 && host.page != "voice") {
		t.Errorf("tool work replayed or protocol arguments executed: writes=%d page=%q, want writes=%d", host.writes, host.page, wantWrites)
	}
}

func TestReplyStreamRejectsProtocolBodyLeak(t *testing.T) {
	for _, tools := range []bool{false, true} {
		for _, body := range protocolLeakBodies {
			for _, split := range []bool{false, true} {
				t.Run(fmt.Sprintf("tools=%t/%s/split=%t", tools, body.name, split), func(t *testing.T) {
					const safePrefix = "已取得工具结果。"
					chunks := []string{safePrefix + body.body}
					if split {
						chunks = []string{safePrefix}
						// Split every marker and argument across real SSE content deltas,
						// including each Unicode pipe, rather than testing a helper alone.
						for _, r := range body.body {
							chunks = append(chunks, string(r))
						}
					}
					e, host, requests := newProtocolLeakFixture(t, true, tools, false, chunks)
					var spoken strings.Builder
					text, err := e.ReplyStream(context.Background(), "请切换到语音页并查询最新资料。", func(delta string) error { spoken.WriteString(delta); return nil })
					wantRequests, wantWrites := int32(1), int32(0)
					if tools {
						wantRequests, wantWrites = 3, 1
					}
					assertProtocolLeakRejected(t, e, host, requests, text, spoken.String(), safePrefix, err, wantRequests, wantWrites)
				})
			}
		}
	}
}

func TestReplyRejectsProtocolBodyLeak(t *testing.T) {
	for _, tools := range []bool{false, true} {
		for _, body := range protocolLeakBodies {
			t.Run(fmt.Sprintf("tools=%t/%s", tools, body.name), func(t *testing.T) {
				e, host, requests := newProtocolLeakFixture(t, false, tools, false, []string{"普通前言。" + body.body})
				text, err := e.Reply(context.Background(), "请切换到语音页并查询最新资料。")
				wantRequests, wantWrites := int32(1), int32(0)
				if tools {
					wantRequests, wantWrites = 2, 1
				}
				assertProtocolLeakRejected(t, e, host, requests, text, "", "", err, wantRequests, wantWrites)
			})
		}
	}
}

func TestReplyStreamRejectsSelectorProtocolBodyWithoutRepair(t *testing.T) {
	for _, body := range protocolLeakBodies {
		t.Run(body.name, func(t *testing.T) {
			e, host, requests := newProtocolLeakFixture(t, true, true, true, []string{body.body})
			var spoken strings.Builder
			text, err := e.ReplyStream(context.Background(), "请切换到语音页并查询最新资料。", func(delta string) error { spoken.WriteString(delta); return nil })
			assertProtocolLeakRejected(t, e, host, requests, text, spoken.String(), "", err, 2, 1)
		})
	}
}

func TestProtocolBodyGuardPreservesOrdinaryText(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tools := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/tools=%t", streaming, tools), func(t *testing.T) {
				chunks := []string{"普通回答：", "2 < 3，", "管道 | 和 ｜；", "<toolbox> 不是工具调用。", "结尾的比较符 <"}
				want := strings.Join(chunks, "")
				e, host, requests := newProtocolLeakFixture(t, streaming, tools, false, chunks)
				var text string
				var err error
				var spoken strings.Builder
				if streaming {
					text, err = e.ReplyStream(context.Background(), "请切换到语音页并回答。", func(delta string) error { spoken.WriteString(delta); return nil })
				} else {
					text, err = e.Reply(context.Background(), "请切换到语音页并回答。")
				}
				if err != nil || text != want || (streaming && spoken.String() != want) {
					t.Fatalf("ordinary text changed: text=%q spoken=%q err=%v, want %q", text, spoken.String(), err, want)
				}
				if len(e.history) != 4 || e.history[3].Content != want {
					t.Fatalf("ordinary answer not committed exactly: %+v", e.history)
				}
				wantRequests, wantWrites := int32(1), 0
				if tools {
					wantRequests, wantWrites = 2, 1
					if streaming {
						wantRequests = 3
					}
				}
				host.mu.Lock()
				defer host.mu.Unlock()
				if requests.Load() != wantRequests || host.writes != wantWrites {
					t.Fatalf("requests=%d writes=%d, want %d/%d", requests.Load(), host.writes, wantRequests, wantWrites)
				}
			})
		}
	}
}
