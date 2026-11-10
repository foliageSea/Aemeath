package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
	"github.com/coder/websocket"
)

type observerHost struct{ writes atomic.Int32 }

func (*observerHost) Metrics() string                            { return "private metrics" }
func (*observerHost) Quota() string                              { return "private quota" }
func (h *observerHost) SetPage(context.Context, string) error    { h.writes.Add(1); return nil }
func (*observerHost) SetScreen(context.Context, bool, int) error { return nil }
func (*observerHost) LockComputer(context.Context) error         { return nil }

// Exercise the real HTTP -> Eino ReAct -> deviceTool -> worker result channel
// path with both speech consumers. Display metadata must never be spoken.
func TestWorkerRealReActToolEvents(t *testing.T) {
	for _, streamingSpeech := range []bool{false, true} {
		t.Run(fmt.Sprint(streamingSpeech), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if requests.Add(1) == 1 {
					io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"random","type":"function","function":{"name":"set_page","arguments":"{\"page\":\"voice\"}"}}]}}]}`)
				} else if req["stream"] == true {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"最终答复。\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"READY"}}]}`)
				}
			}))
			defer server.Close()
			host := &observerHost{}
			engine, err := brain.New(ctx, brain.Config{BaseURL: server.URL, Model: "test", Tools: true}, host)
			if err != nil {
				t.Fatal(err)
			}
			checkText := func(text string) {
				if text != "最终答复。" {
					t.Errorf("metadata entered speech: %q", text)
				}
			}
			var speech speechAPI = &fakeSpeech{synthesize: func(_ context.Context, text, _ string) ([]int16, error) { checkText(text); return []int16{1}, nil }}
			if streamingSpeech {
				speech = &streamTestSpeech{stream: func(ctx context.Context, in <-chan string, audio func([]int16) error) error {
					var text strings.Builder
					for s := range in {
						text.WriteString(s)
					}
					checkText(text.String())
					return audio([]int16{1})
				}}
			}
			dep := fakeDependencies()
			dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { return engine, nil }
			dep.newSpeech = func(Config) speechAPI { return speech }
			jobs := make(chan work, 1)
			results := make(chan result, 16)
			done := make(chan struct{})
			go func() {
				defer close(done)
				worker(ctx, Config{MiMoAPIKey: "secret", LLMModel: "test"}, host, dep, jobs, results)
			}()
			defer func() { cancel(); <-done }()
			jobs <- work{ctx: ctx, turn: 7, generation: 3, kind: "utterance", pcm: []int16{1}}
			var calls []brain.ToolCall
			for {
				select {
				case <-ctx.Done():
					t.Fatal("worker timed out")
				case r := <-results:
					if r.turn != 7 || r.generation != 3 {
						t.Fatalf("missing routing identity: %+v", r)
					}
					if r.err != nil {
						t.Fatalf("worker failure: %v", r.err)
					}
					if r.toolCall != nil {
						calls = append(calls, *r.toolCall)
					}
					if r.phase == "generation_done" {
						if len(calls) != 2 || calls[0].ID != 1 || calls[0].Status != "running" || calls[1].Status != "succeeded" || calls[1].Arguments != `{"page":"voice"}` || host.writes.Load() != 1 {
							t.Fatalf("bad tool events: %+v writes=%d", calls, host.writes.Load())
						}
						return
					}
				}
			}
		})
	}
}

func toolTestSession(t *testing.T) *session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
		for {
			if _, _, err = conn.Read(ctx); err != nil {
				return
			}
		}
	}))
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		cancel()
		server.Close()
		t.Fatal(err)
	}
	peer := <-accepted
	t.Cleanup(func() { cancel(); conn.CloseNow(); peer.CloseNow(); server.Close() })
	return &session{ctx: ctx, ws: conn, state: State{Turn: 1}, active: true, generation: 2, jobs: make(chan work, 1), emit: func(State) {}}
}

func TestSessionToolEventsLifecycleAndOwnership(t *testing.T) {
	s := toolTestSession(t)
	var snapshots []State
	s.emit = func(state State) { snapshots = append(snapshots, state) }
	event := func(turn uint32, generation uint64, id uint64, status string) {
		t.Helper()
		if err := s.result(result{turn: turn, generation: generation, toolCall: &brain.ToolCall{ID: id, Name: "set_page", Status: status}}); err != nil {
			t.Fatal(err)
		}
	}
	event(1, 2, 1, "running")
	first := snapshots[0]
	first.ToolCalls[0].Name = "consumer mutation"
	if s.state.ToolCalls[0].Name != "set_page" {
		t.Fatal("emit aliases session slice")
	}
	event(1, 2, 1, "succeeded")
	if snapshots[0].ToolCalls[0].Status != "running" {
		t.Fatal("later update mutated earlier snapshot")
	}
	for id := uint64(2); id <= 40; id++ {
		event(1, 2, id, "running")
	}
	if len(s.state.ToolCalls) != 32 {
		t.Fatalf("unbounded calls: %d", len(s.state.ToolCalls))
	}
	event(1, 2, 32, "succeeded")
	if s.state.ToolCalls[31].Status != "succeeded" {
		t.Fatal("cap prevented completion update")
	}
	event(0, 2, 1, "failed")
	event(1, 1, 1, "failed")
	if s.state.ToolCalls[0].Status != "succeeded" {
		t.Fatal("stale result applied")
	}
	clone := s.state.Clone()
	clone.ToolCalls[0].Status = "failed"
	if s.state.ToolCalls[0].Status != "succeeded" {
		t.Fatal("Clone aliases tool slice")
	}
	if err := s.command(Command{Kind: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if s.state.ToolCalls[1].Status != "canceled" || s.state.ToolCalls[0].Status != "succeeded" {
		t.Fatal("cancel did not settle only running calls")
	}
	event(1, 2, 2, "succeeded")
	if s.state.ToolCalls[1].Status != "canceled" {
		t.Fatal("inactive result applied")
	}
	if err := s.begin(); err != nil {
		t.Fatal(err)
	}
	if len(s.state.ToolCalls) != 0 {
		t.Fatal("new turn did not clear tools")
	}
	event(1, 2, 1, "running")
	if len(s.state.ToolCalls) != 0 {
		t.Fatal("old turn inserted into new turn")
	}
	event(2, 2, 1, "running")
	// Final model generation can fail while an earlier sentence is synthesizing.
	s.state.Phase = "synthesizing"
	if err := s.fail("thinking", errors.New("test failure")); err != nil {
		t.Fatal(err)
	}
	if s.state.ToolCalls[0].Status != "failed" {
		t.Fatal("failure did not settle tools")
	}
	if s.state.ErrorStage != "thinking" || s.state.Phase != "error" {
		t.Fatalf("failure stage lost: %+v", s.state)
	}
	if err := s.command(Command{Kind: "clear"}); err != nil {
		t.Fatal(err)
	}
	if len(s.state.ToolCalls) != 0 || s.generation != 3 || s.state.ErrorStage != "" {
		t.Fatal("clear did not reset tools, failure stage and generation")
	}
	s.active = true
	event(2, 2, 1, "running")
	if len(s.state.ToolCalls) != 0 {
		t.Fatal("old generation inserted after clear")
	}
}
