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
)

type lockToolHost struct {
	*testHost
	run func(context.Context) error
}

func (h lockToolHost) LockComputer(ctx context.Context) error { return h.run(ctx) }

func TestLockComputerToolValidatesAndReportsSystemResult(t *testing.T) {
	for _, name := range []string{"success", "refused", "cancel_before", "cancel_during", "unknown_args", "null_args"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			h := lockToolHost{testHost: &testHost{}, run: func(c context.Context) error {
				calls++
				if name == "refused" {
					return errors.New("private OS diagnostics")
				}
				if name == "cancel_during" {
					cancel()
					return c.Err()
				}
				return nil
			}}
			var selected *deviceTool
			for _, candidate := range deviceTools(h) {
				d := candidate.(*deviceTool)
				if d.info.Name == "lock_computer" {
					selected = d
					break
				}
			}
			if selected == nil {
				t.Fatal("lock_computer not registered")
			}
			if !strings.Contains(selected.info.Desc, "明确要求电脑锁屏") {
				t.Fatal("missing explicit intent description")
			}
			args := "{}"
			switch name {
			case "cancel_before":
				cancel()
			case "unknown_args":
				args = `{"command":"anything"}`
			case "null_args":
				args = "null"
			}
			result, err := selected.InvokableRun(ctx, args)
			if name == "cancel_before" || name == "cancel_during" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation, got %v", err)
				}
			} else if name == "unknown_args" || name == "null_args" {
				if err == nil {
					t.Fatal("accepted unsupported arguments")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var body struct {
					OK      bool   `json:"ok"`
					Message string `json:"message"`
				}
				if json.Unmarshal([]byte(result), &body) != nil {
					t.Fatal("result is not structured JSON")
				}
				if body.OK != (name == "success") {
					t.Fatalf("reported wrong OS outcome: %s", result)
				}
				if strings.Contains(result, "private OS diagnostics") {
					t.Fatal("raw OS error leaked")
				}
			}
			want := 1
			if name == "cancel_before" || name == "unknown_args" || name == "null_args" {
				want = 0
			}
			if calls != want {
				t.Fatalf("OS calls=%d want=%d", calls, want)
			}
		})
	}
}

func TestReplyStreamInvokesLockComputerExactlyOnce(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			var locks, requests atomic.Int32
			h := lockToolHost{testHost: &testHost{}, run: func(context.Context) error {
				locks.Add(1)
				if refused {
					return errors.New("mock unsupported")
				}
				return nil
			}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req := readRequest(t, r)
				n := requests.Add(1)
				if n == 1 {
					found := false
					for _, entry := range req["tools"].([]any) {
						fn := entry.(map[string]any)["function"].(map[string]any)
						if fn["name"] == "lock_computer" {
							found = true
						}
					}
					if !found {
						t.Error("provider did not receive lock tool schema")
					}
					callsJSON(w, []any{call("lock", "lock_computer", "{}")})
					return
				}
				msgs := req["messages"].([]any)
				found := false
				for _, raw := range msgs {
					m := raw.(map[string]any)
					if m["role"] == "tool" && m["tool_call_id"] == "lock" {
						var v map[string]any
						if json.Unmarshal([]byte(m["content"].(string)), &v) != nil || v["ok"] != !refused {
							t.Error("wrong actual lock result sent to model")
						}
						found = true
					}
				}
				if !found {
					t.Error("missing lock result")
				}
				if n == 2 {
					replyJSON(w, "READY")
					return
				}
				if req["stream"] != true || req["tool_choice"] != "none" {
					t.Error("final reply should stream without tools")
				}
				text := "锁屏请求已提交。"
				if refused {
					text = "锁屏请求未能提交。"
				}
				sseChunk(w, map[string]any{"content": text}, nil)
				sseEnd(w)
			}))
			defer server.Close()
			engine := newEngine(t, server, true, h)
			var spoken strings.Builder
			reply, err := engine.ReplyStream(context.Background(), "请帮我锁定电脑屏幕", func(s string) error { spoken.WriteString(s); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if reply != spoken.String() || locks.Load() != 1 || requests.Load() != 3 {
				t.Fatalf("tool replay or wrong response: locks=%d requests=%d reply=%q", locks.Load(), requests.Load(), reply)
			}
		})
	}
}
