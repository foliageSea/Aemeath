package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/hamcp"
)

// A model tool selection delivered after invalidation must never reach a device or HA.
func TestToolSelectionHTTPRejectsLateCallsAfterLifecycleChange(t *testing.T) {
	const pageArgs = "{\"page\":\"quota\"}"
	for _, streaming := range []bool{false, true} {
		mode := map[bool]string{false: "plain", true: "stream"}[streaming]
		for _, stop := range []string{"clear", "close", "cancel"} {
			t.Run(mode+"/"+stop, func(t *testing.T) {
				host := &toolExecutionTestHost{testHost: &testHost{}}
				var requests, mcpCalls atomic.Int32
				selectionStarted := make(chan context.Context, 1)
				selectionResponded := make(chan struct{})
				releaseSelection := make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseSelection) }) }
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					req := readRequest(t, r)
					if req["stream"] == true || lenOrZero(req["tools"]) == 0 || requests.Load() != 1 {
						t.Error("invalidated turn continued model generation")
						replyJSON(w, "unexpected request")
						return
					}
					defer close(selectionResponded)
					selectionStarted <- r.Context()
					<-releaseSelection
					callsJSON(w, []any{call("page", "set_page", pageArgs), call("read", "get_quota", "{}"), call("ha", haToolName("HassTurnOff"), haValidArgs)})
				}))
				defer server.Close()
				e, err := New(context.Background(), Config{BaseURL: server.URL, Model: "test", Tools: true, HomeAssistant: hamcp.Config{Enabled: true, Token: "ha-secret"}}, host)
				if err != nil {
					t.Fatal(err)
				}
				defer e.Close()
				e.haFactory = func(hamcp.Config) (homeAssistantClient, error) {
					return &fakeHA{discover: func(context.Context) ([]hamcp.Tool, error) { return []hamcp.Tool{haDefinition("HassTurnOff")}, nil }, call: func(context.Context, string, json.RawMessage) (hamcp.Result, error) {
						mcpCalls.Add(1)
						return hamcp.Result{}, nil
					}}, nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				// Always unblock the handler before server.Close, even on a failed assertion.
				defer release()
				type replyResult struct {
					answer, spoken string
					err            error
				}
				done := make(chan replyResult, 1)
				go func() {
					var result replyResult
					if streaming {
						result.answer, result.err = e.ReplyStream(ctx, "请切换到额度页", func(delta string) error {
							result.spoken += delta
							return nil
						})
					} else {
						result.answer, result.err = e.Reply(ctx, "请切换到额度页")
					}
					done <- result
				}()

				var selectionContext context.Context
				select {
				case selectionContext = <-selectionStarted:
				case result := <-done:
					t.Fatalf("reply finished before reaching HTTP tool selection: %+v", result)
				case <-time.After(3 * time.Second):
					t.Fatal("HTTP tool selection did not start")
				}
				switch stop {
				case "clear":
					e.Clear()
				case "close":
					if err := e.Close(); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					cancel()
				}
				if stop == "clear" && !streaming {
					// Plain Clear invalidates the generation without canceling its HTTP
					// request. Deliver late tool calls to exercise the lifecycle guard.
					select {
					case <-selectionContext.Done():
						t.Fatal("plain Clear unexpectedly canceled HTTP tool selection")
					default:
					}
				} else {
					select {
					case <-selectionContext.Done():
					case <-time.After(3 * time.Second):
						t.Fatal("lifecycle change did not cancel HTTP tool selection")
					}
				}
				release()
				select {
				case <-selectionResponded:
				case <-time.After(3 * time.Second):
					t.Fatal("server did not return its late tool selection")
				}
				select {
				case result := <-done:
					if !errors.Is(result.err, context.Canceled) || result.answer != "" || result.spoken != "" {
						t.Fatalf("late tool selection survived lifecycle change: %+v", result)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("invalidated reply did not stop")
				}
				host.mu.Lock()
				writes, locks, page := host.writes, host.locks, host.page
				host.mu.Unlock()
				e.mu.Lock()
				history := len(e.history)
				e.mu.Unlock()
				if writes != 0 || locks != 0 || page != "" || host.reads.Load() != 0 || history != 0 {
					t.Fatalf("invalidated tool selection caused effects: writes=%d locks=%d page=%q reads=%d history=%d", writes, locks, page, host.reads.Load(), history)
				}
				if requests.Load() != 1 || mcpCalls.Load() != 0 {
					t.Fatalf("unexpected lifecycle calls: requests=%d HA=%d", requests.Load(), mcpCalls.Load())
				}
			})
		}
	}
}

func TestLifecycleToolRequiresOnlyCurrentContext(t *testing.T) {
	for _, scenario := range []string{"without_scope", "current_scope", "summary_scope", "background_scope", "stale_generation", "closed", "cancel_without_scope", "cancel_with_scope"} {
		t.Run(scenario, func(t *testing.T) {
			e := &Engine{}
			host := &toolExecutionTestHost{testHost: &testHost{}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario != "without_scope" && scenario != "cancel_without_scope" {
				ctx = context.WithValue(ctx, requestScopeKey{}, requestScope{summary: scenario == "summary_scope", background: scenario == "background_scope"})
			}
			rejected := false
			switch scenario {
			case "stale_generation":
				e.generation++
				rejected = true
			case "closed":
				e.closed = true
				rejected = true
			case "cancel_without_scope", "cancel_with_scope":
				cancel()
				rejected = true
			}
			for _, candidate := range deviceTools(host) {
				raw := candidate.(*deviceTool)
				args := "{}"
				switch raw.info.Name {
				case "get_quota":
				case "set_page":
					args = `{"page":"quota"}`
				default:
					continue
				}
				result, err := (lifecycleTool{InvokableTool: raw, engine: e}).InvokableRun(ctx, args)
				if rejected {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("stale context survived: %q %v", result, err)
					}
				} else if err != nil {
					t.Fatalf("current context requires extra authorization: %q %v", result, err)
				}
			}
			want := int32(1)
			if rejected {
				want = 0
			}
			if host.reads.Load() != want || host.writes != int(want) {
				t.Fatalf("unexpected executions: reads=%d writes=%d want=%d", host.reads.Load(), host.writes, want)
			}
		})
	}
}
