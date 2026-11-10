package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aemeath/agent/internal/timeaware"
	"aemeath/agent/internal/websearch"
)

// A spoken plan must not terminate a request that still needs time and search.
func TestReplyStreamRepairsPlanThenContinuesTimeAndSearch(t *testing.T) {
	var requests, times, searches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		n := requests.Add(1)
		msgs := req["messages"].([]any)
		if n <= 4 && req["stream"] == true {
			t.Error("tool decision streamed before completion")
		}
		switch n {
		case 1:
			prompt := msgs[len(msgs)-1].(map[string]any)["content"].(string)
			if !strings.Contains(prompt, "get_current_time") || !strings.Contains(prompt, "web_search") {
				t.Error("selector excludes time or search")
			}
			replyJSON(w, "我查一下最新的官方公测安排，先确认一下当前日期。")
		case 2:
			if !strings.Contains(msgs[len(msgs)-1].(map[string]any)["content"].(string), "有效工具决策") {
				t.Error("missing decision repair")
			}
			callsJSON(w, []any{call("time", "get_current_time", timeArgs(t, "Asia/Shanghai"))})
		case 3:
			if times.Load() != 1 {
				t.Error("time tool did not run before search decision")
			}
			found := false
			for _, raw := range msgs {
				m := raw.(map[string]any)
				if m["role"] == "tool" && m["tool_call_id"] == "time" {
					report, ok := ParseCurrentTimeReport(m["content"].(string))
					found = ok && report.OK && report.Info.Date == "2026-11-10"
				}
			}
			if !found {
				t.Error("time result lost in continuation")
			}
			callsJSON(w, []any{call("search", "web_search", searchArgs(t, "2026年底 2027年初 二游 官方公测", 1))})
		case 4:
			if searches.Load() != 1 {
				t.Error("search did not run before READY")
			}
			replyJSON(w, "READY")
		case 5:
			if req["stream"] != true || req["tool_choice"] != "none" {
				t.Error("final answer must stream without tools")
			}
			found := false
			for _, raw := range msgs {
				m := raw.(map[string]any)
				if m["role"] == "tool" && m["tool_call_id"] == "search" {
					report, ok := ParseWebSearchReport(m["content"].(string))
					found = ok && report.OK && len(report.Results) == 1
				}
			}
			if !found {
				t.Error("search result lost before final speech")
			}
			sseChunk(w, map[string]any{"role": "assistant", "content": "根据官方公告[1]，公测安排如下。"}, nil)
			sseEnd(w)
		default:
			t.Error("unexpected extra request")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	engine, err := newWithServices(context.Background(), Config{BaseURL: server.URL + "/v1", Model: "test", Tools: true}, &testHost{}, func(ctx context.Context, query string, limit int) (websearch.Response, error) {
		searches.Add(1)
		return websearch.Response{Query: query, Results: []websearch.Result{{Title: "官方公告", URL: "https://example.com/launch", Snippet: "公测时间安排"}}}, nil
	}, func(context.Context, string) (timeaware.Info, error) {
		times.Add(1)
		return fixtureTimeInfo(t, "Asia/Shanghai", "network"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	var spoken string
	answer, err := engine.ReplyStream(context.Background(), "今年年末还有明年年初有什么二游要公测", func(delta string) error { spoken += delta; return nil })
	if err != nil || answer != "根据官方公告[1]，公测安排如下。" || spoken != answer || requests.Load() != 5 || times.Load() != 1 || searches.Load() != 1 {
		t.Fatalf("answer=%q spoken=%q err=%v requests=%d time=%d searches=%d", answer, spoken, err, requests.Load(), times.Load(), searches.Load())
	}
	if len(engine.history) != 2 {
		t.Fatalf("history has %d messages", len(engine.history))
	}
}

func TestReplyStreamRejectsRepeatedInvalidDecisionWithoutReplayingTool(t *testing.T) {
	var requests atomic.Int32
	host := &testHost{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		if req["stream"] == true {
			t.Error("invalid decision reached final speech")
		}
		if requests.Add(1) == 1 {
			callsJSON(w, []any{call("page", "set_page", `{"page":"voice"}`)})
			return
		}
		replyJSON(w, "我接下来查一下。")
	}))
	defer server.Close()
	engine := newEngine(t, server, true, host)
	defer engine.Close()
	spoken := ""
	answer, err := engine.ReplyStream(context.Background(), "切页后查询", func(delta string) error { spoken += delta; return nil })
	assertFailure(t, err, "tool_decision", "invalid_decision", 0)
	if answer != "" || spoken != "" || requests.Load() != 3 || len(engine.history) != 0 || host.writes != 1 {
		t.Fatalf("answer=%q spoken=%q requests=%d history=%d writes=%d", answer, spoken, requests.Load(), len(engine.history), host.writes)
	}
}
