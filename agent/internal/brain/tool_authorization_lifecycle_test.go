package brain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the real HTTP authorization request, not the authorizeTool test seam:
// an ALLOW returned after the turn is invalidated must never reach the Host.
func TestToolAuthorizationHTTPRejectsLateAllowAfterLifecycleChange(t *testing.T) {
	const pageArgs = "{\"page\":\"quota\"}"
	for _, streaming := range []bool{false, true} {
		mode := map[bool]string{false: "plain", true: "stream"}[streaming]
		for _, stop := range []string{"clear", "close", "cancel"} {
			t.Run(mode+"/"+stop, func(t *testing.T) {
				host := &authorizationTestHost{testHost: &testHost{}}
				var requests, selections, checks atomic.Int32
				authorizationStarted := make(chan context.Context, 1)
				authorizationResponded := make(chan struct{})
				releaseAuthorization := make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseAuthorization) }) }
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					req := readRequest(t, r)
					if req["tool_choice"] == "none" && req["stream"] != true {
						if checks.Add(1) != 1 {
							t.Error("authorization was unexpectedly retried")
							replyJSON(w, "DENY")
							return
						}
						defer close(authorizationResponded)
						user, proposed := authorizationPayloadForTest(t, req)
						if user != "请切换到额度页" || proposed["name"] != "set_page" || proposed["arguments"] != pageArgs {
							t.Errorf("wrong lifecycle authorization proposal: %q %v", user, proposed)
						}
						authorizationStarted <- r.Context()
						<-releaseAuthorization
						replyJSON(w, "ALLOW")
						return
					}
					if selections.Add(1) != 1 || req["stream"] == true || lenOrZero(req["tools"]) == 0 {
						t.Error("invalidated turn continued model generation")
						if req["stream"] == true {
							sseChunk(w, map[string]any{"content": "不应生成的最终回答"}, nil)
							sseEnd(w)
						} else {
							replyJSON(w, "不应生成的最终回答")
						}
						return
					}
					callsJSON(w, []any{call("page", "set_page", pageArgs)})
				}))
				defer server.Close()
				e, err := New(context.Background(), Config{BaseURL: server.URL, Model: "test", Tools: true}, host)
				if err != nil {
					t.Fatal(err)
				}
				defer e.Close()
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

				var authorizationContext context.Context
				select {
				case authorizationContext = <-authorizationStarted:
				case result := <-done:
					t.Fatalf("reply finished before reaching HTTP authorization: %+v", result)
				case <-time.After(3 * time.Second):
					t.Fatal("HTTP authorization did not start")
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
					// request. Deliver a real late ALLOW to exercise the post-check guard.
					select {
					case <-authorizationContext.Done():
						t.Fatal("plain Clear unexpectedly canceled HTTP authorization")
					default:
					}
				} else {
					select {
					case <-authorizationContext.Done():
					case <-time.After(3 * time.Second):
						t.Fatal("lifecycle change did not cancel HTTP authorization")
					}
				}
				release()
				select {
				case <-authorizationResponded:
				case <-time.After(3 * time.Second):
					t.Fatal("server did not return its late ALLOW")
				}
				select {
				case result := <-done:
					if !errors.Is(result.err, context.Canceled) || result.answer != "" || result.spoken != "" {
						t.Fatalf("late ALLOW survived lifecycle change: %+v", result)
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
					t.Fatalf("invalidated authorization caused effects: writes=%d locks=%d page=%q reads=%d history=%d", writes, locks, page, host.reads.Load(), history)
				}
				if requests.Load() != 2 || selections.Load() != 1 || checks.Load() != 1 {
					t.Fatalf("unexpected lifecycle requests: total=%d selections=%d checks=%d", requests.Load(), selections.Load(), checks.Load())
				}
			})
		}
	}
}
