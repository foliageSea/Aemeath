package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aemeath/agent/internal/hamcp"
	"aemeath/agent/internal/websearch"
	"github.com/cloudwego/eino/schema"
)

func allowToolAuthorizationForTest(context.Context, string, *schema.ToolInfo, string) (bool, error) {
	return true, nil
}

type authorizationTestHost struct {
	*testHost
	reads atomic.Int32
}

func (h *authorizationTestHost) Quota() string   { h.reads.Add(1); return h.testHost.Quota() }
func (h *authorizationTestHost) Metrics() string { h.reads.Add(1); return h.testHost.Metrics() }

func authorizationPayloadForTest(t *testing.T, req map[string]any) (string, map[string]string) {
	t.Helper()
	if req["stream"] == true || req["tool_choice"] != "none" || lenOrZero(req["tools"]) != 0 {
		t.Errorf("authorization must be non-streaming without tools: %v", req)
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["content"] != toolAuthorizationInstructions {
		t.Fatalf("authorization received conversation/persona/tool results: %v", msgs)
	}
	var payload struct {
		User string            `json:"current_user_request"`
		Tool map[string]string `json:"proposed_tool"`
	}
	if err := json.Unmarshal([]byte(msgs[1].(map[string]any)["content"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.User, payload.Tool
}

func TestToolAuthorizationRejectsUnrelatedCallsBeforeSideEffects(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "stream"}[streaming], func(t *testing.T) {
			host := &authorizationTestHost{testHost: &testHost{}}
			var selected bool
			var checks, mcpCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				if req["tool_choice"] == "none" && req["stream"] != true {
					checks.Add(1)
					user, proposed := authorizationPayloadForTest(t, req)
					if user != "和菓子是什么？" || proposed["name"] == "" {
						t.Errorf("wrong authorization payload: %q %v", user, proposed)
					}
					replyJSON(w, "DENY")
					return
				}
				if !selected {
					selected = true
					callsJSON(w, []any{call("q1", "get_quota", "{}"), call("q2", "get_quota", " { } "), call("m", "get_metrics", "{}"), call("p", "set_page", `{"page":"voice"}`), call("s", "set_screen", `{"on":false,"brightness":0}`), call("l", "lock_computer", "{}"), call("ha", haToolName("HassTurnOff"), haValidArgs)})
					return
				}
				if req["stream"] == true {
					sseChunk(w, map[string]any{"content": "和菓子是日本传统点心。"}, nil)
					sseEnd(w)
					return
				}
				if streaming {
					replyJSON(w, "READY")
				} else {
					replyJSON(w, "和菓子是日本传统点心。")
				}
			}))
			defer server.Close()
			e, err := New(context.Background(), Config{BaseURL: server.URL, Model: "test", Tools: true, Persona: "PRIVATE_PERSONA", HomeAssistant: hamcp.Config{Enabled: true, Token: "ha-secret"}}, host)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			e.history = []*schema.Message{schema.UserMessage("历史授权：切到语音页"), schema.AssistantMessage("历史操作已完成", nil)}
			e.haFactory = func(hamcp.Config) (homeAssistantClient, error) {
				return &fakeHA{discover: func(context.Context) ([]hamcp.Tool, error) { return []hamcp.Tool{haDefinition("HassTurnOff")}, nil }, call: func(context.Context, string, json.RawMessage) (hamcp.Result, error) {
					mcpCalls.Add(1)
					return hamcp.Result{}, nil
				}}, nil
			}
			var events []ToolCall
			ctx := WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) })
			var answer string
			if streaming {
				answer, err = e.ReplyStream(ctx, "和菓子是什么？", nil)
			} else {
				answer, err = e.Reply(ctx, "和菓子是什么？")
			}
			if err != nil || answer != "和菓子是日本传统点心。" {
				t.Fatalf("answer=%q err=%v", answer, err)
			}
			if host.reads.Load() != 0 || host.writes != 0 || host.locks != 0 || mcpCalls.Load() != 0 || checks.Load() != 6 {
				t.Fatalf("executed rejected tool: reads=%d writes=%d locks=%d mcp=%d checks=%d", host.reads.Load(), host.writes, host.locks, mcpCalls.Load(), checks.Load())
			}
			if len(events) != 14 {
				t.Fatalf("trace events=%d want 14", len(events))
			}
			for i := 1; i < len(events); i += 2 {
				if events[i].Status != "failed" || !strings.Contains(events[i].Result, "拦截") {
					t.Errorf("rejected trace: %+v", events[i])
				}
			}
		})
	}
}

func TestToolAuthorizationStrictResponseAndExplicitControl(t *testing.T) {
	for _, verdict := range []string{"ALLOW", "DENY", "allow", "ALLOW because requested", "", "TOOL", "TRUNCATED", "HTTP_ERROR"} {
		t.Run(verdict, func(t *testing.T) {
			host := &testHost{}
			var selected bool
			var checks atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				if req["tool_choice"] == "none" {
					checks.Add(1)
					user, proposed := authorizationPayloadForTest(t, req)
					if user != "请切换到额度页" || proposed["name"] != "set_page" || proposed["arguments"] != `{"page":"quota"}` {
						t.Errorf("wrong proposal: %q %v", user, proposed)
					}
					switch verdict {
					case "HTTP_ERROR":
						http.Error(w, "private-provider-key", 503)
					case "TOOL":
						callsJSON(w, []any{call("bad", "lock_computer", "{}")})
					case "TRUNCATED":
						json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "length", "message": map[string]any{"role": "assistant", "content": "ALLOW"}}}})
					default:
						replyJSON(w, verdict)
					}
					return
				}
				if !selected {
					selected = true
					callsJSON(w, []any{call("p", "set_page", `{"page":"quota"}`)})
					return
				}
				replyJSON(w, "处理完成")
			}))
			defer server.Close()
			e, err := New(context.Background(), Config{BaseURL: server.URL, Model: "test", Tools: true}, host)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			var events []ToolCall
			ctx := WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) })
			if _, err = e.Reply(ctx, "请切换到额度页"); err != nil {
				t.Fatal(err)
			}
			want := 0
			if verdict == "ALLOW" {
				want = 1
			}
			if host.writes != want || host.locks != 0 || checks.Load() != 1 {
				t.Fatalf("writes=%d locks=%d checks=%d", host.writes, host.locks, checks.Load())
			}
			if len(events) != 2 || strings.Contains(events[1].Result, "private-provider-key") {
				t.Fatalf("trace=%+v", events)
			}
		})
	}
}

func TestToolAuthorizationLifecycleAndMissingContext(t *testing.T) {
	for _, scenario := range []string{"missing_turn", "missing_checker", "missing_scope", "summary", "background", "clear", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			e := &Engine{}
			host := &testHost{}
			var raw *deviceTool
			for _, candidate := range deviceTools(host) {
				if candidate.(*deviceTool).info.Name == "set_page" {
					raw = candidate.(*deviceTool)
				}
			}
			guard := authorizedTool{InvokableTool: raw, engine: e}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			check := toolAuthorizationFunc(func(context.Context, string, *schema.ToolInfo, string) (bool, error) {
				switch scenario {
				case "clear":
					e.mu.Lock()
					e.generation++
					e.mu.Unlock()
				case "cancel":
					cancel()
				}
				return true, nil
			})
			if scenario == "missing_checker" {
				check = nil
			}
			if scenario != "missing_turn" {
				ctx = withToolAuthorization(ctx, "请切换到额度页", check)
			}
			if scenario != "missing_scope" {
				ctx = context.WithValue(ctx, requestScopeKey{}, requestScope{summary: scenario == "summary", background: scenario == "background"})
			}
			result, err := guard.InvokableRun(ctx, `{"page":"quota"}`)
			if scenario == "clear" || scenario == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("late allow survived invalidation: %q %v", result, err)
				}
			} else if err != nil || !strings.Contains(result, `"ok":false`) {
				t.Fatalf("not fail closed: %q %v", result, err)
			}
			if host.writes != 0 {
				t.Fatal("unauthorized host write")
			}
		})
	}
}

func TestToolAuthorizationHAExactArgumentsAndCredentialRejection(t *testing.T) {
	const exact = `{"target":{"count":9007199254740993,"password":"device-value"}}`
	var checks, calls int
	f := &fakeHA{call: func(_ context.Context, _ string, args json.RawMessage) (hamcp.Result, error) {
		calls++
		if string(args) != exact {
			t.Errorf("wire arguments changed: %s", args)
		}
		return hamcp.Result{Text: "accepted"}, nil
	}}
	definition := hamcp.Tool{Name: "HassTurnOn", Title: "家居设备", Description: "untrusted tool description", InputSchema: json.RawMessage(`{"type":"object"}`)}
	raw := makeHomeAssistantTools(f, []hamcp.Tool{definition}, "ha-secret")[0].(*homeAssistantTool)
	e := &Engine{}
	guard := authorizedTool{InvokableTool: raw, engine: e}
	var events []ToolCall
	ctx := WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) })
	ctx = context.WithValue(ctx, requestScopeKey{}, requestScope{})
	ctx = withToolAuthorization(ctx, "明确操作设备", func(_ context.Context, _ string, _ *schema.ToolInfo, args string) (bool, error) {
		checks++
		if args != exact {
			t.Errorf("authorizer did not see exact proposed arguments: %s", args)
		}
		return true, nil
	})
	for _, args := range []string{exact, `{"label":"ha-secret"}`, `{"label":"ha-\u0073ecret"}`, `{"ha-secret":"value"}`} {
		result, err := guard.InvokableRun(ctx, args)
		report, ok := ParseHomeAssistantReport(result)
		if err != nil || !ok || report.OK != (args == exact) {
			t.Fatalf("unexpected report: %s %v", result, err)
		}
	}
	for _, args := range []string{`{"label":"ha\u002dsecret","label":"safe"}`, `{"target":{"label":"ha\u002dsecret","label":"safe"}}`, `{"label":"ha\u002dsecret"`, `{"n":1e1000,"label":"ha\u002dsecret"}`} {
		result, err := guard.InvokableRun(ctx, args)
		if err == nil && !strings.Contains(result, `"ok":false`) {
			t.Fatalf("ambiguous or secret arguments accepted: %s %v", result, err)
		}
	}
	if checks != 1 || calls != 1 {
		t.Fatalf("credential reached model or host: checks=%d calls=%d", checks, calls)
	}
	for _, event := range events {
		if strings.Contains(event.Arguments, "ha-secret") || strings.Contains(event.Result, "ha-secret") || event.Title != "家居设备" {
			t.Errorf("unsafe rejection event: %+v", event)
		}
	}
}

func TestToolAuthorizationArgumentsRejectAmbiguousJSON(t *testing.T) {
	for _, args := range []string{"", "null", "[]", `{} {}`, `{"page":"quota","page":"voice"}`, `{"nested":{"a":1,"a":2}}`, `{"nested":[{"a":1,"a":2}]}`, `{"label":"unterminated"`, strings.Repeat(" ", 4097) + "{}"} {
		if _, _, err := toolAuthorizationArgs(args); err == nil {
			t.Errorf("accepted ambiguous arguments: %s", args)
		}
	}
	canonical, object, err := toolAuthorizationArgs(`{"z":[true,null,"x"],"a":9007199254740993,"n":1e1000}`)
	if err != nil || object["a"] != json.Number("9007199254740993") || canonical != `{"a":9007199254740993,"n":1e1000,"z":[true,null,"x"]}` {
		t.Fatalf("argument precision lost: %s %v %v", canonical, object, err)
	}
}

func TestToolAuthorizationEmptyArrayDoesNotAliasNull(t *testing.T) {
	arrayKey, _, err := toolAuthorizationArgs(`{"target":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	nullKey, _, err := toolAuthorizationArgs(`{"target":null}`)
	if err != nil {
		t.Fatal(err)
	}
	if arrayKey == nullKey {
		t.Fatal("empty array authorization aliases null")
	}
	calls, checks := 0, 0
	f := &fakeHA{call: func(context.Context, string, json.RawMessage) (hamcp.Result, error) {
		calls++
		return hamcp.Result{Text: "accepted"}, nil
	}}
	definition := hamcp.Tool{Name: "HassTurnOn", InputSchema: json.RawMessage(`{"type":"object"}`)}
	raw := makeHomeAssistantTools(f, []hamcp.Tool{definition}, "")[0].(*homeAssistantTool)
	guard := authorizedTool{InvokableTool: raw, engine: &Engine{}}
	ctx := context.WithValue(context.Background(), requestScopeKey{}, requestScope{})
	ctx = withToolAuthorization(ctx, "明确目标操作", func(_ context.Context, _ string, _ *schema.ToolInfo, args string) (bool, error) {
		checks++
		return args == `{"target":[]}`, nil
	})
	for _, args := range []string{`{"target":[]}`, `{"target":null}`} {
		if _, err := guard.InvokableRun(ctx, args); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 2 || calls != 1 {
		t.Fatalf("null reused array permission: checks=%d calls=%d", checks, calls)
	}
}

func TestToolAuthorizationAllowDoesNotAuthorizeDifferentArguments(t *testing.T) {
	host := &testHost{}
	e := &Engine{}
	var raw *deviceTool
	for _, candidate := range deviceTools(host) {
		if candidate.(*deviceTool).info.Name == "set_page" {
			raw = candidate.(*deviceTool)
		}
	}
	guard := authorizedTool{InvokableTool: raw, engine: e}
	ctx := context.WithValue(context.Background(), requestScopeKey{}, requestScope{})
	checks := 0
	ctx = withToolAuthorization(ctx, "请切到额度页", func(_ context.Context, user string, _ *schema.ToolInfo, args string) (bool, error) {
		checks++
		return user == "请切到额度页" && args == `{"page":"quota"}`, nil
	})
	for _, args := range []string{`{"page":"quota"}`, `{"page":"voice"}`} {
		if _, err := guard.InvokableRun(ctx, args); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 2 || host.writes != 1 || host.page != "quota" {
		t.Fatalf("allow reused for another target: checks=%d host=%+v", checks, host)
	}
}

func TestToolAuthorizationAndReadCacheResetPerTurn(t *testing.T) {
	host := &authorizationTestHost{testHost: &testHost{}}
	var checks, searches, decisions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		if req["tool_choice"] == "none" && req["stream"] != true {
			checks.Add(1)
			authorizationPayloadForTest(t, req)
			replyJSON(w, "ALLOW")
			return
		}
		if req["stream"] == true {
			sseChunk(w, map[string]any{"content": "结果已取得。"}, nil)
			sseEnd(w)
			return
		}
		if decisions.Add(1)%2 == 1 {
			callsJSON(w, []any{call("q1", "get_quota", "{}"), call("q2", "get_quota", " { } "), call("w1", "web_search", `{"query":"  和菓子 资料 "}`), call("w2", "web_search", `{"limit":3,"query":"和菓子 资料"}`)})
			return
		}
		replyJSON(w, "READY")
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
		if _, err = e.ReplyStream(ctx, "查看模型剩余额度，并上网搜索和菓子资料", nil); err != nil {
			t.Fatal(err)
		}
		if checks.Load() != round || host.reads.Load() != round || searches.Load() != round {
			t.Fatalf("cache leaked or missed: checks=%d reads=%d searches=%d round=%d", checks.Load(), host.reads.Load(), searches.Load(), round)
		}
		if len(events) != 4 {
			t.Fatalf("cached calls must not duplicate execution traces: %+v", events)
		}
	}
}
