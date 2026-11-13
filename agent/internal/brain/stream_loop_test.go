package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/websearch"

	"github.com/cloudwego/eino/schema"
)

func assertToolSelectionPromptRules(t *testing.T, prompt string) {
	t.Helper()
	for _, rule := range []string{
		"常识问答或闲聊若已有知识足够回答",
		"直接只输出 READY，无需先调用任何工具",
		"所有工具",
		"与用户当前请求直接相关且确有必要时调用",
		"设备或 Home Assistant 控制必须有用户当前明确请求",
		"历史已完成操作不代表当前授权",
		"已有充分结果时不要重复调用",
		"实时搜索任务",
		"实际工具调用",
		"不能只输出计划",
	} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("tool selection prompt missing rule %q: %s", rule, prompt)
		}
	}
}

func TestReplyStreamCommonKnowledgeReadyWithoutTools(t *testing.T) {
	for _, tc := range []struct {
		name, question, answer string
		completedControl       bool
	}{
		{name: "common_knowledge", question: "为什么天空是蓝色的？", answer: "因为空气分子对蓝光的散射更强。"},
		{name: "small_talk", question: "你好，聊聊天吧。", answer: "你好，很高兴和你聊天。"},
		{name: "after_completed_control", question: "为什么天空是蓝色的？", answer: "因为空气分子对蓝光的散射更强。", completedControl: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, searches atomic.Int32
			host := &testHost{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				msgs := req["messages"].([]any)
				for _, raw := range msgs {
					msg := raw.(map[string]any)
					if msg["role"] == "tool" || lenOrZero(msg["tool_calls"]) != 0 {
						t.Errorf("knowledge reply must not execute tools: %v", msg)
					}
				}
				switch requests.Add(1) {
				case 1:
					if req["stream"] == true || lenOrZero(req["tools"]) == 0 {
						t.Error("first request must select with tools available, without streaming")
					}
					selector := msgs[len(msgs)-1].(map[string]any)
					if selector["role"] != "system" {
						t.Error("selector must be a private system instruction")
					}
					assertToolSelectionPromptRules(t, selector["content"].(string))
					replyJSON(w, "READY")
				case 2:
					if req["stream"] != true || req["tool_choice"] != "none" || lenOrZero(req["tools"]) != 0 {
						t.Error("first READY must immediately stream the final answer without tools")
					}
					last := msgs[len(msgs)-1].(map[string]any)
					if last["role"] != "user" || last["content"] != tc.question {
						t.Errorf("final request lost the current question: %v", last)
					}
					for _, raw := range msgs {
						if text, ok := raw.(map[string]any)["content"].(string); ok && strings.Contains(text, "READY") {
							t.Error("private tool selection leaked into final input")
						}
					}
					sseChunk(w, map[string]any{"role": "assistant", "content": tc.answer}, nil)
					sseEnd(w)
				default:
					t.Error("unexpected extra request after first READY")
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			engine, err := newWithWebSearch(context.Background(), Config{BaseURL: server.URL + "/v1", Model: "test", Persona: "测试助手", Tools: true}, host, func(context.Context, string, int) (websearch.Response, error) {
				searches.Add(1)
				t.Error("knowledge reply unexpectedly called web_search")
				return websearch.Response{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			if tc.completedControl {
				engine.history = []*schema.Message{
					schema.UserMessage("切换到语音页面"),
					schema.AssistantMessage("切页指令已发送。", nil),
				}
			}
			priorHistory := len(engine.history)
			var spoken string
			answer, err := engine.ReplyStream(context.Background(), tc.question, func(delta string) error { spoken += delta; return nil })
			if err != nil || answer != tc.answer || spoken != answer || requests.Load() != 2 || searches.Load() != 0 || host.writes != 0 || host.locks != 0 {
				t.Fatalf("answer=%q spoken=%q err=%v requests=%d searches=%d writes=%d locks=%d", answer, spoken, err, requests.Load(), searches.Load(), host.writes, host.locks)
			}
			if len(engine.history) != priorHistory+2 || engine.history[len(engine.history)-1].Content != tc.answer {
				t.Fatalf("final answer not committed as one history turn: %v", engine.history)
			}
		})
	}
}

// A spoken plan must not terminate a request that still needs search grounded in the snapshot.
func TestReplyStreamRepairsPlanThenSearchesUsingTimeSnapshot(t *testing.T) {
	var requests, searches atomic.Int32
	now := time.Date(2026, 11, 10, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		assertTimeSnapshotRequest(t, req, timeSnapshotPrompt(now))
		n := requests.Add(1)
		msgs := req["messages"].([]any)
		if n <= 3 && req["stream"] == true {
			t.Error("tool decision streamed before completion")
		}
		switch n {
		case 1:
			prompt := msgs[len(msgs)-1].(map[string]any)["content"].(string)
			assertToolSelectionPromptRules(t, prompt)
			if !strings.Contains(prompt, "web_search") {
				t.Error("selector excludes search")
			}
			replyJSON(w, "我查一下最新的官方公测安排。")
		case 2:
			prompt := msgs[len(msgs)-1].(map[string]any)["content"].(string)
			assertToolSelectionPromptRules(t, prompt)
			if !strings.Contains(prompt, "有效工具决策") {
				t.Error("missing decision repair")
			}
			callsJSON(w, []any{call("search", "web_search", searchArgs(t, "2026年底 2027年初 二游 官方公测", 1))})
		case 3:
			if searches.Load() != 1 {
				t.Error("search did not run before READY")
			}
			replyJSON(w, "READY")
		case 4:
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
	engine, err := newWithWebSearch(context.Background(), Config{BaseURL: server.URL + "/v1", Model: "test", Persona: "测试助手", Tools: true}, &testHost{}, func(ctx context.Context, query string, limit int) (websearch.Response, error) {
		searches.Add(1)
		return websearch.Response{Query: query, Results: []websearch.Result{{Title: "官方公告", URL: "https://example.com/launch", Snippet: "公测时间安排"}}}, nil

	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engine.timeNow = func() time.Time { return now }
	var spoken string
	answer, err := engine.ReplyStream(context.Background(), "今年年末还有明年年初有什么二游要公测", func(delta string) error { spoken += delta; return nil })
	if err != nil || answer != "根据官方公告[1]，公测安排如下。" || spoken != answer || requests.Load() != 4 || searches.Load() != 1 {
		t.Fatalf("answer=%q spoken=%q err=%v requests=%d searches=%d", answer, spoken, err, requests.Load(), searches.Load())
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
