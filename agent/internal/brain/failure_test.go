package brain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/compose"
)

func assertFailure(t *testing.T, err error, stage, code string, status int) {
	t.Helper()
	var failure *requestFailure
	if !errors.As(err, &failure) || failure.stage != stage || failure.code != code || failure.status != status {
		t.Fatalf("failure=%+v (%v), want %s/%s HTTP %d", failure, err, stage, code, status)
	}
	if errors.Unwrap(err) != nil || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "private-provider") {
		t.Fatalf("unsafe public error: %v", err)
	}
}

func TestHTTPFailureCategoriesAcrossModelStages(t *testing.T) {
	statuses := []struct {
		status int
		code   string
	}{{400, "invalid_request"}, {401, "authentication"}, {403, "permission"}, {404, "model_not_found"}, {408, "timeout"}, {422, "invalid_request"}, {429, "rate_limit"}, {500, "service_unavailable"}, {504, "timeout"}}
	for _, mode := range []string{"plain", "decision", "final"} {
		for _, tc := range statuses {
			t.Run(fmt.Sprintf("%s/%d", mode, tc.status), func(t *testing.T) {
				var requests atomic.Int32
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					req := readRequest(t, r)
					requests.Add(1)
					if mode == "final" && req["stream"] != true {
						replyJSON(w, "READY")
						return
					}
					w.WriteHeader(tc.status)
					// Deliberately non-JSON: classify by status, not provider text.
					io.WriteString(w, "private-provider secret-key authentication stream max steps")
				}))
				defer s.Close()
				e := newEngine(t, s, mode != "plain", &testHost{})
				defer e.Close()
				var err error
				stage, wantRequests := "model", int32(1)
				if mode == "plain" {
					_, err = e.Reply(context.Background(), "test")
				} else {
					stage = "tool_decision"
					if mode == "final" {
						stage, wantRequests = "final_reply", 2
					}
					_, err = e.ReplyStream(context.Background(), "test", func(string) error { t.Error("failed request emitted speech"); return nil })
				}
				assertFailure(t, err, stage, tc.code, tc.status)
				if requests.Load() != wantRequests || len(e.history) != 0 {
					t.Fatalf("requests=%d history=%d; request must not retry or commit", requests.Load(), len(e.history))
				}
			})
		}
	}
}

func TestSafeFailureClassificationNeverUsesRawText(t *testing.T) {
	assertFailure(t, safeRequestFailure(fmt.Errorf("graph: %w", compose.ErrExceedMaxSteps), "final_reply"), "agent", "step_limit", 0)
	assertFailure(t, safeRequestFailure(errors.New("private-provider 401 max steps secret-key"), "model"), "model", "invalid_response", 0)
	assertFailure(t, safeRequestFailure(context.DeadlineExceeded, "tool_decision"), "tool_decision", "timeout", 0)
	if err := safeRequestFailure(context.Canceled, "final_reply"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestToolExecutionFailureCategory(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequest(t, r)
		requests.Add(1)
		callsJSON(w, []any{call("invalid", "set_page", "{}")})
	}))
	defer s.Close()
	host := &testHost{}
	e := newEngine(t, s, true, host)
	defer e.Close()
	_, err := e.ReplyStream(context.Background(), "test", nil)
	assertFailure(t, err, "tool_execution", "tool_execution", 0)
	if requests.Load() != 1 || host.writes != 0 || len(e.history) != 0 {
		t.Fatalf("requests=%d writes=%d history=%d", requests.Load(), host.writes, len(e.history))
	}
}
