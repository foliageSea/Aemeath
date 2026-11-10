package voice

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
)

// A full result channel must not leave the LLM producer or its ReAct tool
// observer blocked while segmentedSpeech is joining its goroutine on cancel.
func TestSegmentedSpeechToolObserverBackpressureCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"one","type":"function","function":{"name":"get_metrics","arguments":"{}"}}]}}]}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine, err := brain.New(ctx, brain.Config{BaseURL: server.URL, Model: "test", Tools: true}, &observerHost{})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- segmentedSpeech(ctx, engine, &fakeSpeech{}, "读取指标", "", func(c context.Context, r result) bool {
			if r.toolCall != nil && r.toolCall.Status == "running" {
				close(entered)
			}
			// Simulate a bounded result channel with no available consumer.
			<-c.Done()
			return false
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("tool observer did not start")
	}
	cancel()
	if err := pipelineTestResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
