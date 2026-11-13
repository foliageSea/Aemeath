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
	"time"
	"unicode/utf8"

	"aemeath/agent/internal/hamcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeHA struct {
	discover func(context.Context) ([]hamcp.Tool, error)
	call     func(context.Context, string, json.RawMessage) (hamcp.Result, error)
	closed   atomic.Int32
}

func (f *fakeHA) Discover(ctx context.Context) ([]hamcp.Tool, error) { return f.discover(ctx) }
func (f *fakeHA) Call(ctx context.Context, n string, a json.RawMessage) (hamcp.Result, error) {
	return f.call(ctx, n, a)
}
func (f *fakeHA) Close() error { f.closed.Add(1); return nil }
func haDefinition(name string) hamcp.Tool {
	return hamcp.Tool{Name: name, Title: "客厅灯", Description: "按用户要求操作灯", InputSchema: json.RawMessage(haNestedSchema)}
}

const haNestedSchema = `{"type":"object","required":["target"],"properties":{"target":{"$ref":"#/$defs/target"}},"$defs":{"target":{"type":"object","required":["room","level"],"properties":{"room":{"type":"string","enum":["living","bed"]},"level":{"type":"integer","minimum":0,"maximum":100}}}},"additionalProperties":false}`
const haValidArgs = `{"target":{"room":"living","level":42}}`

func newHAEngine(t *testing.T, s *httptest.Server, f *fakeHA) *Engine {
	t.Helper()
	e, err := New(context.Background(), Config{BaseURL: s.URL, Model: "mock", Tools: true, HomeAssistant: hamcp.Config{Enabled: true, Token: "ha-secret"}}, &testHost{})
	if err != nil {
		t.Fatal(err)
	}
	e.haFactory = func(hamcp.Config) (homeAssistantClient, error) { return f, nil }
	e.authorizeTool = allowToolAuthorizationForTest // isolate MCP/schema behavior
	t.Cleanup(func() { _ = e.Close() })
	return e
}
func TestHomeAssistantSchemaValidationTraceAndReport(t *testing.T) {
	var calls int
	f := &fakeHA{call: func(ctx context.Context, name string, args json.RawMessage) (hamcp.Result, error) {
		calls++
		if name != "HassTurnOn" {
			t.Errorf("wrong remote name %q", name)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 20*time.Second {
			t.Error("missing call budget")
		}
		return hamcp.Result{Text: "ha-secret " + strings.Repeat("中", 5000)}, nil
	}}
	tools := makeHomeAssistantTools(f, []hamcp.Tool{haDefinition("HassTurnOn")}, "ha-secret")
	if len(tools) != 1 {
		t.Fatalf("tools=%d", len(tools))
	}
	tool := tools[0].(*homeAssistantTool)
	var events []ToolCall
	ctx := WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) })
	for _, bad := range []string{"[]", "null", "{}", `{"target":{"room":"garage","level":42}}`, `{"target":{"room":"living","level":"42"}}`, `{"target":{"room":"living","level":42.5}}`, `{"target":{"room":"living"}}`, `{"target":{"room":"living","level":101}}`, strings.Repeat(" ", 4097) + "{}"} {
		out, err := tool.InvokableRun(ctx, bad)
		r, ok := ParseHomeAssistantReport(out)
		if err != nil || !ok || r.OK {
			t.Errorf("invalid args accepted: %s %s %v", bad, out, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid arguments reached MCP")
	}
	out, err := tool.InvokableRun(ctx, haValidArgs)
	r, ok := ParseHomeAssistantReport(out)
	if err != nil || !ok || !r.OK || !r.Truncated || len(r.Text) > 8192 || !utf8.ValidString(r.Text) || strings.Contains(out, "ha-secret") {
		t.Fatalf("unsafe report %+v err=%v", r, err)
	}
	last := events[len(events)-1]
	if last.Title != "客厅灯" || last.Status != "succeeded" || last.Arguments != haSafeArguments(haValidArgs, "") || last.Millis < 1 {
		t.Fatalf("trace %+v", last)
	}
	if events[1].Status != "failed" {
		t.Fatal("invalid arguments trace succeeded")
	}
	f.call = func(context.Context, string, json.RawMessage) (hamcp.Result, error) {
		return hamcp.Result{IsError: true, Text: "设备不存在"}, nil
	}
	out, _ = tool.InvokableRun(ctx, haValidArgs)
	if r, ok := ParseHomeAssistantReport(out); !ok || r.OK || events[len(events)-1].Status != "failed" {
		t.Fatal("MCP isError was treated as success")
	}
	if Parse, ok := ParseHomeAssistantReport(`{"tool":"ha_bad"}`); ok {
		t.Fatalf("accepted missing OK: %+v", Parse)
	}
}

func TestHomeAssistantNamesAndInvalidSchemas(t *testing.T) {
	definitions := []hamcp.Tool{haDefinition("a.b"), haDefinition("a/b"), haDefinition(strings.Repeat("x", 100) + "a"), haDefinition(strings.Repeat("x", 100) + "b")}
	for i, s := range []string{`{"type":"object","properties":{"x":{"type":"nonsense"}}}`, `{"type":"object","required":"x"}`, `{"type":"object","properties":{"x":{"$ref":"https://example.invalid/schema"}}}`, `{"type":"object","properties":{"x":{"$ref":"#/$defs/missing"}}}`, `{"type":"array"}`, `{"type":"object","description":"ha-\u0073ecret"}`} {
		d := haDefinition(string(rune('0' + i)))
		d.InputSchema = json.RawMessage(s)
		definitions = append(definitions, d)
	}
	got := makeHomeAssistantTools(&fakeHA{}, definitions, "ha-secret")
	if len(got) != 4 {
		for _, entry := range got {
			t.Logf("registered %s", entry.(*homeAssistantTool).original)
		}
		t.Fatalf("invalid schema registered: %d", len(got))
	}
	seen := map[string]bool{}
	for _, tool := range got {
		info, _ := tool.Info(context.Background())
		if len(info.Name) > 64 || !haNamePattern.MatchString(info.Name) || seen[info.Name] {
			t.Errorf("invalid/colliding name %q", info.Name)
		}
		seen[info.Name] = true
	}
}

func TestHomeAssistantDynamicDiscoveryRecoveryAndHistoryRealEino(t *testing.T) {
	var discovery int
	f := &fakeHA{discover: func(ctx context.Context) ([]hamcp.Tool, error) {
		discovery++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 8*time.Second {
			t.Error("missing discovery budget")
		}
		switch discovery {
		case 1:
			return []hamcp.Tool{haDefinition("HassTurnOn")}, nil
		case 2:
			return []hamcp.Tool{haDefinition("HassTurnOff"), haDefinition("GetLiveContext")}, nil
		case 3:
			return nil, errors.New("raw ha-secret endpoint")
		default:
			return []hamcp.Tool{haDefinition("GetLiveContext")}, nil
		}
	}, call: func(context.Context, string, json.RawMessage) (hamcp.Result, error) {
		return hamcp.Result{Text: "accepted"}, nil
	}}
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		n := int(requests.Add(1))
		all := req["tools"].([]any)
		expected := []int{7, 8, 6, 7}[n-1]
		if len(all) != expected {
			t.Errorf("round %d tools=%d want=%d", n, len(all), expected)
		}
		names := map[string]bool{}
		for _, v := range all {
			fn := v.(map[string]any)["function"].(map[string]any)
			names[fn["name"].(string)] = true
			if strings.HasPrefix(fn["name"].(string), "ha_") {
				params := fn["parameters"].(map[string]any)
				if params["$defs"] == nil || params["required"] == nil {
					t.Error("nested schema lost in OpenAI request")
				}
			}
		}
		if n > 1 && names[haToolName("HassTurnOn")] {
			t.Error("removed HA tool survived rebuild")
		}
		msgs := req["messages"].([]any)
		if len(msgs) != 2*n {
			t.Errorf("round %d history count=%d", n, len(msgs))
		}
		raw, _ := json.Marshal(req)
		if strings.Contains(string(raw), "ha-secret") {
			t.Error("token entered model")
		}
		if n == 3 && !strings.Contains(string(raw), "连接或工具发现失败") {
			t.Error("missing offline state prompt")
		}
		replyJSON(w, "回答")
	}))
	defer s.Close()
	e := newHAEngine(t, s, f)
	if discovery != 0 {
		t.Fatal("New performed discovery")
	}
	var statuses []hamcp.Status
	ctx := WithHomeAssistantObserver(context.Background(), func(status hamcp.Status) { statuses = append(statuses, status) })
	for i := 0; i < 4; i++ {
		if _, err := e.Reply(ctx, "状态"); err != nil {
			t.Fatal(err)
		}
	}
	if len(statuses) != 4 || statuses[2].Connected || statuses[2].ToolCount != 0 || !statuses[3].Connected || statuses[3].ToolCount != 1 {
		t.Fatalf("statuses %+v", statuses)
	}
	if len(e.history) != 8 {
		t.Fatal("rebuild lost history")
	}
	e.Clear()
	if len(e.history) != 0 {
		t.Fatal("Clear lost original semantics")
	}
}

func TestHomeAssistantCallFailureNoRetryAndMaxStepRealEino(t *testing.T) {
	for _, loop := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "step_limit"}[loop], func(t *testing.T) {
			var calls, requests atomic.Int32
			f := &fakeHA{discover: func(context.Context) ([]hamcp.Tool, error) { return []hamcp.Tool{haDefinition("HassTurnOn")}, nil }, call: func(context.Context, string, json.RawMessage) (hamcp.Result, error) {
				calls.Add(1)
				return hamcp.Result{}, errors.New("ha-secret transport")
			}}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				n := requests.Add(1)
				raw, _ := json.Marshal(req)
				if strings.Contains(string(raw), "ha-secret") {
					t.Error("secret leaked to model")
				}
				if loop || n <= 2 {
					callsJSON(w, []any{call("ha-call", haToolName("HassTurnOn"), haValidArgs)})
				} else {
					replyJSON(w, "执行状态未知")
				}
			}))
			defer s.Close()
			e := newHAEngine(t, s, f)
			var events []ToolCall
			var statuses []hamcp.Status
			ctx := WithToolObserver(WithHomeAssistantObserver(context.Background(), func(s hamcp.Status) { statuses = append(statuses, s) }), func(c ToolCall) { events = append(events, c) })
			_, err := e.Reply(ctx, "开灯")
			if loop && err == nil || !loop && err != nil {
				t.Fatalf("loop=%v err=%v", loop, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("repeated uncertain action %d", calls.Load())
			}
			if requests.Load() > maxSteps {
				t.Fatalf("step limit ignored %d", requests.Load())
			}
			if len(statuses) != 2 || statuses[1].Connected {
				t.Fatalf("missing connection failure status %+v", statuses)
			}
			if len(events) < 4 || events[1].Status != "failed" || events[3].Status != "failed" {
				t.Fatalf("failed calls became successful: %+v", events)
			}
			if loop && len(e.history) != 0 {
				t.Fatal("failed turn entered history")
			}
		})
	}
}

func TestHomeAssistantCloseCancelsDiscoveryAndQueuedReply(t *testing.T) {
	entered := make(chan struct{})
	f := &fakeHA{discover: func(ctx context.Context) ([]hamcp.Tool, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("canceled discovery reached model") }))
	defer s.Close()
	e := newHAEngine(t, s, f)
	done := make(chan error, 2)
	go func() { _, err := e.Reply(context.Background(), "one"); done <- err }()
	<-entered
	go func() { _, err := e.Reply(context.Background(), "two"); done <- err }()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not cancel active/queued reply")
		}
	}
	if f.closed.Load() != 1 {
		t.Fatalf("client close count=%d", f.closed.Load())
	}
}

func TestHomeAssistantClearDuringDiscovery(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "reply", true: "stream"}[streaming], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			f := &fakeHA{discover: func(ctx context.Context) ([]hamcp.Tool, error) {
				close(entered)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return nil, nil
				}
			}}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("cleared discovery sent an obsolete model request")
				replyJSON(w, "完成")
			}))
			defer s.Close()
			e := newHAEngine(t, s, f)
			done := make(chan error, 1)
			go func() {
				var err error
				if streaming {
					_, err = e.ReplyStream(context.Background(), "hi", nil)
				} else {
					_, err = e.Reply(context.Background(), "hi")
				}
				done <- err
			}()
			<-entered
			e.Clear()
			close(release)
			err := <-done
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err=%v", err)
			}
			if len(e.history) != 0 {
				t.Fatal("Clear during discovery did not invalidate history commit")
			}
		})
	}
}

func TestHomeAssistantRealMCPAndEino(t *testing.T) {
	var online atomic.Bool
	online.Store(true)
	var mcpCalls atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-ha", Version: "1"}, &mcp.ServerOptions{Instructions: "REMOTE_PROMPT_MUST_NOT_ENTER_MODEL ha-secret"})
	add := func(name string) {
		server.AddTool(&mcp.Tool{Name: name, Title: "真实HA标题", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			mcpCalls.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "accepted ha-secret"}}}, nil
		})
	}
	add("HassTurnOn")
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ha-secret" {
			t.Error("missing HA authorization")
		}
		if !online.Load() {
			http.Error(w, "offline ha-secret", 503)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer backend.Close()
	var expected atomic.Value
	expected.Store("HassTurnOn")
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readRequest(t, r)
		raw, _ := json.Marshal(req)
		if strings.Contains(string(raw), "ha-secret") || strings.Contains(string(raw), "REMOTE_PROMPT_MUST_NOT_ENTER_MODEL") {
			t.Error("credential/remote prompt entered model")
		}
		count := len(req["tools"].([]any))
		if !online.Load() {
			if count != 6 {
				t.Errorf("offline tool count=%d", count)
			}
			replyJSON(w, "HA不可用")
			return
		}
		if count != 7 {
			t.Errorf("online tool count=%d", count)
		}
		messages := req["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		if last["role"] == "tool" {
			report, ok := ParseHomeAssistantReport(last["content"].(string))
			if !ok || !report.OK {
				t.Errorf("bad MCP report %+v", report)
			}
			replyJSON(w, "HA已接受调用，物理状态未确认")
			return
		}
		callsJSON(w, []any{call("sdk-call", haToolName(expected.Load().(string)), "{}")})
	}))
	defer model.Close()
	e, err := New(context.Background(), Config{BaseURL: model.URL, Model: "mock", Tools: true, HomeAssistant: hamcp.Config{Enabled: true, Endpoint: backend.URL, Token: "ha-secret"}}, &testHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.authorizeTool = allowToolAuthorizationForTest // isolate real MCP discovery/recovery
	var events []ToolCall
	ctx := WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) })
	for round := 0; round < 4; round++ {
		switch round {
		case 1:
			server.RemoveTools("HassTurnOn")
			add("HassTurnOff")
			expected.Store("HassTurnOff")
		case 2:
			online.Store(false)
		case 3:
			online.Store(true)
		}
		if _, err := e.Reply(ctx, "按我要求操作灯"); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
	if mcpCalls.Load() != 3 {
		t.Fatalf("MCP calls=%d", mcpCalls.Load())
	}
	for _, event := range events {
		if event.Title != "真实HA标题" {
			t.Errorf("title not propagated %+v", event)
		}
	}
	if len(e.history) != 8 {
		t.Fatal("real MCP recovery lost history")
	}
}

func TestHomeAssistantCloseCancelsActiveCall(t *testing.T) {
	entered := make(chan struct{})
	f := &fakeHA{discover: func(context.Context) ([]hamcp.Tool, error) { return []hamcp.Tool{haDefinition("HassTurnOn")}, nil }, call: func(ctx context.Context, _ string, _ json.RawMessage) (hamcp.Result, error) {
		close(entered)
		<-ctx.Done()
		return hamcp.Result{}, ctx.Err()
	}}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsJSON(w, []any{call("cancel-call", haToolName("HassTurnOn"), haValidArgs)})
	}))
	defer s.Close()
	e := newHAEngine(t, s, f)
	var events []ToolCall
	done := make(chan error, 1)
	go func() {
		_, err := e.Reply(WithToolObserver(context.Background(), func(c ToolCall) { events = append(events, c) }), "开灯")
		done <- err
	}()
	<-entered
	e.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel MCP call")
	}
	if len(events) != 2 || events[1].Status != "canceled" || len(e.history) != 0 {
		t.Fatalf("events=%+v history=%d", events, len(e.history))
	}
}

func TestHomeAssistantDisabledNeverConnects(t *testing.T) {
	for _, tools := range []bool{false, true} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { replyJSON(w, "ok") }))
		e, err := New(context.Background(), Config{BaseURL: s.URL, Model: "mock", Tools: tools, HomeAssistant: hamcp.Config{Enabled: !tools}}, &testHost{})
		if err != nil {
			t.Fatal(err)
		}
		e.haFactory = func(hamcp.Config) (homeAssistantClient, error) {
			t.Error("HA connected when disabled")
			return nil, errors.New("disabled")
		}
		if _, err := e.Reply(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		e.Close()
		s.Close()
	}
}

func TestHomeAssistantSafeArgumentDisplay(t *testing.T) {
	input := `{"nested":{"token":"abc","password":"p","value":"ha-secret"},"ha-secret":"bad","text":"hello"}`
	output := haSafeArguments(input, "ha-secret")
	if strings.Contains(output, "ha-secret") || strings.Contains(output, "abc") || !json.Valid([]byte(output)) {
		t.Fatalf("unsafe args %s", output)
	}
	for _, s := range []string{"null", "[]", "{}{}"} {
		if got := toolArguments("ha_test", s); got != "" {
			t.Errorf("nonobject args shown: %q", got)
		}
	}
	if got := toolArguments("ha_test", `{"room":"living"}`); got != `{"room":"living"}` {
		t.Errorf("HA args absent: %s", got)
	}
}
