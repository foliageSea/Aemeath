package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"", ""},
		{" \t\r\n", ""},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{" LOW ", "low"},
		{"\tMeDiUm\n", "medium"},
		{" HIGH ", "high"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizeReasoningEffort(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeReasoningEffort(%q) = %q, %v; want %q, nil", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestNormalizeReasoningEffortRejectsInvalidWithGenericError(t *testing.T) {
	var message string
	for _, input := range []string{"none", "minimal", "max", "low high", "private-invalid-effort-token"} {
		t.Run(input, func(t *testing.T) {
			got, err := NormalizeReasoningEffort(input)
			if err == nil || got != "" {
				t.Fatalf("NormalizeReasoningEffort(%q) = %q, %v; want empty result and error", input, got, err)
			}
			if message == "" {
				message = err.Error()
			}
			if err.Error() == "" || err.Error() != message {
				t.Errorf("invalid values must return the same nonempty generic error: %q vs %q", err.Error(), message)
			}
			if strings.Contains(err.Error(), input) {
				t.Errorf("error contains invalid input: %q", err)
			}
		})
	}
}

func TestReasoningEffortOnEveryWireRequest(t *testing.T) {
	for _, effort := range []struct {
		name  string
		input string
		want  string
	}{
		{"default", "", ""},
		{"whitespace_default", " \t\r\n", ""},
		{"low", " LOW ", "low"},
		{"medium", "\tMeDiUm\n", "medium"},
		{"high", "high", "high"},
	} {
		t.Run(effort.name, func(t *testing.T) {
			for _, mode := range []struct {
				name   string
				stream bool
				tools  bool
			}{
				{"Reply", false, false},
				{"ReplyStream", true, false},
				{"ReplyStreamWithTools", true, true},
			} {
				t.Run(mode.name, func(t *testing.T) {
					var requests atomic.Int32
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						req := readRequest(t, r)
						n := requests.Add(1)
						got, present := req["reasoning_effort"]
						if effort.want == "" {
							if present {
								t.Errorf("request %d: default reasoning_effort must be omitted, got %#v", n, got)
							}
						} else if !present || got != effort.want {
							t.Errorf("request %d: reasoning_effort = %#v (present=%t), want %q", n, got, present, effort.want)
						}
						if mode.tools && n == 1 {
							if req["stream"] == true || lenOrZero(req["tools"]) == 0 {
								t.Error("expected a nonstreaming tool decision request")
							}
							replyJSON(w, "READY")
							return
						}
						if mode.stream {
							if req["stream"] != true {
								t.Error("expected final SSE request")
							}
							sseChunk(w, map[string]any{"role": "assistant", "content": "hello"}, nil)
							sseEnd(w)
							return
						}
						if req["stream"] == true {
							t.Error("plain Reply unexpectedly requested a stream")
						}
						replyJSON(w, "hello")
					}))
					defer s.Close()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					e, err := New(ctx, Config{
						BaseURL: s.URL + "/v1", APIKey: "test-key", Model: "mock-model",
						Persona: "test assistant", ReasoningEffort: effort.input, Tools: mode.tools,
					}, &testHost{})
					if err != nil {
						t.Fatal(err)
					}
					defer e.Close()
					var got string
					if mode.stream {
						var deltas strings.Builder
						got, err = e.ReplyStream(ctx, "say hello", func(delta string) error {
							deltas.WriteString(delta)
							return nil
						})
						if deltas.String() != "hello" {
							t.Errorf("streamed deltas = %q, want hello", deltas.String())
						}
					} else {
						got, err = e.Reply(ctx, "say hello")
					}
					if err != nil || got != "hello" {
						t.Fatalf("reply = %q, %v; want hello, nil", got, err)
					}
					wantRequests := int32(1)
					if mode.tools {
						wantRequests = 2
					}
					if got := requests.Load(); got != wantRequests {
						t.Errorf("request count = %d, want %d", got, wantRequests)
					}
				})
			}
		})
	}
}
