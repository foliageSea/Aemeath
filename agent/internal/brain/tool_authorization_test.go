package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aemeath/agent/internal/hamcp"
	"aemeath/agent/internal/websearch"
)

type toolExecutionTestHost struct {
	*testHost
	reads atomic.Int32
}

func (h *toolExecutionTestHost) Quota() string   { h.reads.Add(1); return h.testHost.Quota() }
func (h *toolExecutionTestHost) Metrics() string { h.reads.Add(1); return h.testHost.Metrics() }

func TestToolsExecuteWithoutAuthorizationRequests(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "stream"}[streaming], func(t *testing.T) {
			host := &toolExecutionTestHost{testHost: &testHost{}}
			var requests, selections, mcpCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				req := readRequest(t, r)
				if req["stream"] == true {
					sseChunk(w, map[string]any{"content": "操作已提交。"}, nil)
					sseEnd(w)
					return
				}
				if lenOrZero(req["tools"]) == 0 {
					t.Error("unexpected tool-free auxiliary model request")
				}
				if selections.Add(1) == 1 {
					callsJSON(w, []any{call("q1", "get_quota", "{}"), call("q2", "get_quota", " { } "), call("m", "get_metrics", "{}"), call("p", "set_page", `{"page":"voice"}`), call("s", "set_screen", `{"on":false,"brightness":0}`), call("l", "lock_computer", "{}"), call("ha", haToolName("HassTurnOff"), haValidArgs)})
					return
				}
				if streaming {
					replyJSON(w, "READY")
				} else {
					replyJSON(w, "操作已提交。")
				}
			}))
			defer server.Close()
			e, err := New(context.Background(), Config{BaseURL: server.URL, Model: "test", Tools: true, HomeAssistant: hamcp.Config{Enabled: true, Token: "ha-secret"}}, host)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			e.haFactory = func(hamcp.Config) (homeAssistantClient, error) {
				return &fakeHA{discover: func(context.Context) ([]hamcp.Tool, error) { return []hamcp.Tool{haDefinition("HassTurnOff")}, nil }, call: func(_ context.Context, name string, args json.RawMessage) (hamcp.Result, error) {
					mcpCalls.Add(1)
					if name != "HassTurnOff" || string(args) != haValidArgs {
						t.Errorf("unexpected HA call: %s %s", name, args)
					}
					return hamcp.Result{Text: "accepted"}, nil
				}}, nil
			}
			var events []ToolCall
			ctx := WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) })
			var answer string
			if streaming {
				answer, err = e.ReplyStream(ctx, "查看系统指标和额度，切到语音页、关闭屏幕、锁定电脑并关闭客厅灯", nil)
			} else {
				answer, err = e.Reply(ctx, "查看系统指标和额度，切到语音页、关闭屏幕、锁定电脑并关闭客厅灯")
			}
			if err != nil || answer != "操作已提交。" {
				t.Fatalf("answer=%q err=%v", answer, err)
			}
			if host.reads.Load() != 2 || host.writes != 2 || host.locks != 1 || host.page != "voice" || host.on || host.brightness != 0 || mcpCalls.Load() != 1 {
				t.Fatalf("tool calls missing: reads=%d writes=%d locks=%d page=%q screen=%t/%d HA=%d", host.reads.Load(), host.writes, host.locks, host.page, host.on, host.brightness, mcpCalls.Load())
			}
			wantRequests := int32(2)
			if streaming {
				wantRequests = 3
			}
			if requests.Load() != wantRequests || selections.Load() != 2 {
				t.Fatalf("extra model requests: total=%d selections=%d want=%d/2", requests.Load(), selections.Load(), wantRequests)
			}
			if len(events) != 12 {
				t.Fatalf("execution traces=%d want 12 (duplicate read is cached)", len(events))
			}
			for i := 1; i < len(events); i += 2 {
				if events[i].Status != "succeeded" {
					t.Errorf("tool failed: %+v", events[i])
				}
			}
		})
	}
}

func TestReadOnlyToolCacheResetsEachTurnWithoutAuthorization(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "stream"}[streaming], func(t *testing.T) {
			host := &toolExecutionTestHost{testHost: &testHost{}}
			var searches, decisions, requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				req := readRequest(t, r)
				if req["stream"] == true {
					sseChunk(w, map[string]any{"content": "结果已取得。"}, nil)
					sseEnd(w)
					return
				}
				if lenOrZero(req["tools"]) == 0 {
					t.Error("unexpected tool-free auxiliary model request")
				}
				if decisions.Add(1)%2 == 1 {
					callsJSON(w, []any{call("q1", "get_quota", "{}"), call("q2", "get_quota", " { } "), call("w1", "web_search", `{"query":"  和菓子 资料 "}`), call("w2", "web_search", `{"limit":3,"query":"和菓子 资料"}`)})
					return
				}
				if streaming {
					replyJSON(w, "READY")
				} else {
					replyJSON(w, "结果已取得。")
				}
			}))
			defer server.Close()
			e, err := newWithWebSearch(context.Background(), Config{BaseURL: server.URL, Model: "test", Tools: true}, host, func(_ context.Context, q string, _ int) (websearch.Response, error) {
				searches.Add(1)
				return websearch.Response{Query: q, Results: []websearch.Result{{Title: "资料", URL: "https://example.com", Snippet: "点心"}}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			for round := int32(1); round <= 2; round++ {
				var events []ToolCall
				ctx := WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) })
				var answer string
				if streaming {
					answer, err = e.ReplyStream(ctx, "查看额度，并搜索和菓子资料", nil)
				} else {
					answer, err = e.Reply(ctx, "查看额度，并搜索和菓子资料")
				}
				if err != nil || answer != "结果已取得。" {
					t.Fatalf("answer=%q err=%v", answer, err)
				}
				if host.reads.Load() != round || searches.Load() != round {
					t.Fatalf("cache leaked or missed: reads=%d searches=%d round=%d", host.reads.Load(), searches.Load(), round)
				}
				if len(events) != 4 {
					t.Fatalf("cached calls duplicated execution traces: %+v", events)
				}
				perTurn := int32(2)
				if streaming {
					perTurn = 3
				}
				if requests.Load() != round*perTurn {
					t.Fatalf("extra model requests: %d want %d", requests.Load(), round*perTurn)
				}
			}
		})
	}
}

func TestToolArgumentsRejectAmbiguousJSON(t *testing.T) {
	for _, args := range []string{"", "null", "[]", `{} {}`, `{"page":"quota","page":"voice"}`, `{"nested":{"a":1,"a":2}}`, `{"nested":[{"a":1,"a":2}]}`, `{"label":"unterminated"`, strings.Repeat(" ", 4097) + "{}"} {
		if _, _, err := parseToolArguments(args); err == nil {
			t.Errorf("accepted ambiguous arguments: %s", args)
		}
	}
	canonical, object, err := parseToolArguments(`{"z":[true,null,"x"],"a":9007199254740993,"n":1e1000}`)
	if err != nil || object["a"] != json.Number("9007199254740993") || canonical != `{"a":9007199254740993,"n":1e1000,"z":[true,null,"x"]}` {
		t.Fatalf("argument precision lost: %s %v %v", canonical, object, err)
	}
	arrayKey, _, err := parseToolArguments(`{"target":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	nullKey, _, err := parseToolArguments(`{"target":null}`)
	if err != nil {
		t.Fatal(err)
	}
	if arrayKey == nullKey {
		t.Fatal("empty array aliases null")
	}
}

func TestHomeAssistantExactArgumentsAndCredentialRejectionWithoutAuthorization(t *testing.T) {
	const exact = `{"target":{"count":9007199254740993,"password":"device-value"}}`
	calls := 0
	f := &fakeHA{call: func(_ context.Context, _ string, args json.RawMessage) (hamcp.Result, error) {
		calls++
		if string(args) != exact {
			t.Errorf("wire arguments changed: %s", args)
		}
		return hamcp.Result{Text: "accepted"}, nil
	}}
	definition := hamcp.Tool{Name: "HassTurnOn", Title: "家居设备", Description: "untrusted tool description", InputSchema: json.RawMessage(`{"type":"object"}`)}
	raw := makeHomeAssistantTools(f, []hamcp.Tool{definition}, "ha-secret")[0].(*homeAssistantTool)
	var events []ToolCall
	ctx := WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) })
	for _, args := range []string{exact, `{"label":"ha-secret"}`, `{"label":"ha-\u0073ecret"}`, `{"ha-secret":"value"}`, `{"label":"ha\u002dsecret","label":"safe"}`, `{"target":{"label":"ha\u002dsecret","label":"safe"}}`, `{"label":"ha\u002dsecret"`, `{"n":1e1000,"label":"ha\u002dsecret"}`} {
		result, err := raw.InvokableRun(ctx, args)
		report, ok := ParseHomeAssistantReport(result)
		if err != nil || !ok || report.OK != (args == exact) {
			t.Fatalf("unexpected report for %s: %s %v", args, result, err)
		}
	}
	if calls != 1 {
		t.Fatalf("unsafe arguments reached host: calls=%d", calls)
	}
	for _, event := range events {
		if strings.Contains(event.Arguments, "ha-secret") || strings.Contains(event.Result, "ha-secret") || event.Title != "家居设备" {
			t.Errorf("unsafe rejection event: %+v", event)
		}
	}
}
