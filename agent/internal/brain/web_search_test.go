package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aemeath/agent/internal/websearch"
)

func searchArgs(t *testing.T, query string, limit int) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"query": query, "limit": limit})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWebSearchToolResultsAndFailureEvents(t *testing.T) {
	for _, mode := range []string{"success", "empty", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var events []ToolCall
			ctx = WithToolObserver(ctx, func(call ToolCall) { events = append(events, call) })
			calls := 0
			search := func(ctx context.Context, query string, limit int) (websearch.Response, error) {
				calls++
				if query != "ESP32 S3 资料" || limit != 3 {
					t.Fatalf("query=%q limit=%d", query, limit)
				}
				if mode == "failure" {
					return websearch.Response{}, errors.New("private upstream error sk-secret")
				}
				if mode == "cancel" {
					cancel()
					return websearch.Response{}, ctx.Err()
				}
				response := websearch.Response{Query: query, Results: []websearch.Result{}}
				if mode == "success" {
					response.Results = []websearch.Result{{Title: "ESP32-S3 官方文档", URL: "https://docs.espressif.com/projects/esp-idf/", Snippet: "开发文档与示例。"}}
				}
				return response, nil
			}
			output, err := webSearchTool(search).InvokableRun(ctx, searchArgs(t, " ESP32  S3 资料 ", 0))
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) || len(events) != 2 || events[1].Status != "canceled" {
					t.Fatalf("cancellation lost: %v %+v", err, events)
				}
				return
			}
			if err != nil || calls != 1 || len(events) != 2 || events[0].Status != "running" {
				t.Fatalf("output=%s err=%v calls=%d events=%+v", output, err, calls, events)
			}
			if strings.Contains(output, "sk-secret") || strings.Contains(events[1].Result, "private") {
				t.Fatal("raw upstream error exposed")
			}
			report, ok := ParseWebSearchReport(output)
			if !ok {
				t.Fatalf("invalid report %s", output)
			}
			if mode == "failure" {
				if report.OK || events[1].Status != "failed" || events[1].Result == "" {
					t.Fatal("failed search pretended to succeed")
				}
			} else {
				shown, ok := ParseWebSearchReport(events[1].Result)
				if !report.OK || !ok || events[1].Status != "succeeded" || len(shown.Results) != len(report.Results) {
					t.Fatal("model and display sources differ")
				}
			}
		})
	}
}

func TestTavilySearchFailuresAreActionableWithoutExposingCredentials(t *testing.T) {
	for _, test := range []struct {
		err  error
		text string
	}{
		{websearch.ErrAPIKey, "Tavily API 密钥"},
		{websearch.ErrUnauthorized, "无效或权限不足"},
		{websearch.ErrRateLimit, "过于频繁"},
		{websearch.ErrQuota, "额度或用量上限"},
	} {
		tool := webSearchTool(func(context.Context, string, int) (websearch.Response, error) { return websearch.Response{}, test.err })
		output, err := tool.InvokableRun(context.Background(), searchArgs(t, "test", 1))
		report, ok := ParseWebSearchReport(output)
		if err != nil || !ok || report.OK || !strings.Contains(report.Message, test.text) {
			t.Fatalf("missing actionable Tavily error: %s %v", output, err)
		}
	}
	output, err := webSearchTool(nil).InvokableRun(context.Background(), searchArgs(t, "test", 1))
	report, ok := ParseWebSearchReport(output)
	if err != nil || !ok || report.OK || !strings.Contains(report.Message, "Tavily API 密钥") {
		t.Fatal("unconfigured search did not return setup guidance")
	}
}

func TestWebSearchToolRejectsInvalidArgumentsAndUnsafeSources(t *testing.T) {
	searches := 0
	search := func(context.Context, string, int) (websearch.Response, error) {
		searches++
		return websearch.Response{Results: []websearch.Result{{Title: "bad", URL: "javascript:alert(1)"}}}, nil
	}
	for _, args := range []string{"{}", searchArgs(t, "", 3), searchArgs(t, strings.Repeat("查", 257), 3), searchArgs(t, "test", 6)} {
		output, err := webSearchTool(search).InvokableRun(context.Background(), args)
		report, ok := ParseWebSearchReport(output)
		if err != nil || !ok || report.OK {
			t.Fatalf("invalid args accepted: %s", args)
		}
	}
	if searches != 0 {
		t.Fatal("invalid query reached provider")
	}
	output, err := webSearchTool(search).InvokableRun(context.Background(), searchArgs(t, "test", 1))
	report, ok := ParseWebSearchReport(output)
	if err != nil || !ok || report.OK || len(report.Results) != 0 {
		t.Fatal("unsafe source reached model/display")
	}
}

func TestWebSearchRealReActAndStreamOnlySpeakFinalAnswer(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var requests, searches atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				n := requests.Add(1)
				msgs := req["messages"].([]any)
				if !strings.Contains(msgs[0].(map[string]any)["content"].(string), "外部不可信资料") {
					t.Error("missing search trust boundary")
				}
				if n == 1 {
					callsJSON(w, []any{call("search-call", "web_search", searchArgs(t, "ESP32-S3 文档", 2))})
					return
				}
				found := false
				for _, raw := range msgs {
					message := raw.(map[string]any)
					if message["role"] == "tool" {
						report, ok := ParseWebSearchReport(message["content"].(string))
						if ok && report.OK && len(report.Results) == 1 && report.Results[0].URL == "https://docs.espressif.com/" {
							found = true
						}
					}
				}
				if !found {
					t.Error("search sources did not reach ReAct model")
				}
				if req["stream"] == true {
					sseChunk(w, map[string]any{"role": "assistant", "content": "根据官方文档[1]，可以查看开发指南。"}, nil)
					sseEnd(w)
				} else {
					if streaming {
						replyJSON(w, "READY")
					} else {
						replyJSON(w, "根据官方文档[1]，可以查看开发指南。")
					}
				}
			}))
			defer server.Close()
			engine, err := newWithWebSearch(context.Background(), Config{BaseURL: server.URL + "/v1", Model: "test", Tools: true}, &testHost{}, func(ctx context.Context, q string, n int) (websearch.Response, error) {
				searches.Add(1)
				return websearch.Response{Query: q, Results: []websearch.Result{{Title: "官方文档", URL: "https://docs.espressif.com/", Snippet: "芯片开发指南"}}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var answer, deltas string
			if streaming {
				answer, err = engine.ReplyStream(context.Background(), "搜索官方文档", func(delta string) error { deltas += delta; return nil })
			} else {
				answer, err = engine.Reply(context.Background(), "搜索官方文档")
			}
			if err != nil || searches.Load() != 1 || !strings.Contains(answer, "官方文档[1]") {
				t.Fatalf("answer=%q err=%v searches=%d", answer, err, searches.Load())
			}
			if streaming && (deltas != answer || strings.Contains(deltas, "https://")) {
				t.Fatal("source payload leaked into speech stream")
			}
		})
	}
}
