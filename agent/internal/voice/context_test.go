package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
)

func TestContextWindowConfiguration(t *testing.T) {
	for _, tc := range []struct{ input, want int }{{0, 32768}, {8192, 8192}, {65536, 65536}, {2097152, 2097152}} {
		cfg, err := NormalizeConfig(Config{ContextWindow: tc.input})
		if err != nil || cfg.ContextWindow != tc.want {
			t.Fatalf("context window %d: got %d, err %v", tc.input, cfg.ContextWindow, err)
		}
	}
	for _, input := range []int{-1, 8191, 2097153} {
		if _, err := NormalizeConfig(Config{ContextWindow: input}); err == nil {
			t.Fatalf("accepted invalid context window %d while voice disabled", input)
		}
	}
	var cfg Config
	if err := json.Unmarshal([]byte(`{"contextWindow":65536}`), &cfg); err != nil || cfg.ContextWindow != 65536 {
		t.Fatalf("contextWindow JSON decoding: %+v, %v", cfg, err)
	}
	data, err := json.Marshal(State{Context: brain.ContextStats{EstimatedTokens: 1024, UsageAvailable: true}})
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	var stats map[string]any
	if err := json.Unmarshal(state["context"], &stats); err != nil || stats["estimatedTokens"] != float64(1024) || stats["usageAvailable"] != true {
		t.Fatalf("context stats JSON bridge: %s, %v", data, err)
	}
}

func TestProductionBrainReceivesContextWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("brain construction must not send HTTP requests") }))
	defer server.Close()
	engine, err := productionDependencies().newBrain(context.Background(), Config{LLMBaseURL: server.URL, LLMModel: "test", ContextWindow: 65536}, nil)
	if err != nil {
		t.Fatal(err)
	}
	source, ok := engine.(interface {
		ContextStats() brain.ContextStats
		Close() error
	})
	if !ok {
		t.Fatal("production engine does not expose context stats")
	}
	defer source.Close()
	if got := source.ContextStats().ContextWindow; got != 65536 {
		t.Fatalf("brain.New lost configured context window: %d", got)
	}
}

type contextTestBrain struct {
	fakeBrain
	stats brain.ContextStats
}

func (b *contextTestBrain) ContextStats() brain.ContextStats { return b.stats }

func TestWorkerPublishesEngineContextStats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	want := brain.ContextStats{ContextWindow: 65536, EstimatedTokens: 2048, InputBudget: 60000, TotalTokens: 900, UsageAvailable: true}
	dep := fakeDependencies()
	dep.newBrain = func(_ context.Context, cfg Config, _ Host) (brainAPI, error) {
		if cfg.ContextWindow != want.ContextWindow {
			t.Errorf("worker lost configured capacity: %d", cfg.ContextWindow)
		}
		return &contextTestBrain{stats: want}, nil
	}
	jobs, out := make(chan work, 1), make(chan result, 32)
	go func() {
		defer close(done)
		worker(ctx, Config{MiMoAPIKey: "mimo", LLMModel: "model", ContextWindow: want.ContextWindow}, nil, dep, jobs, out)
	}()
	jobs <- work{ctx: ctx, turn: 1, kind: "utterance", pcm: []int16{1}}
	for {
		select {
		case r := <-out:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.phase == "response" {
				if r.context == nil || *r.context != want {
					t.Fatalf("missing engine context statistics: %+v", r.context)
				}
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatal("worker failed to publish a response")
		}
	}
}

func TestContextResultAfterClearAndStaleGenerations(t *testing.T) {
	var snapshots []State
	s := &session{generation: 2, state: State{Turn: 3, Phase: "idle"}, emit: func(state State) { snapshots = append(snapshots, state) }}
	want := brain.ContextStats{ContextWindow: 32768, RetainedTurns: 0}
	if err := s.result(result{turn: 3, generation: 2, phase: "context", context: &want}); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].Context != want || s.state.Phase != "idle" {
		t.Fatalf("clear did not publish idle context statistics: %+v", snapshots)
	}
	stale := brain.ContextStats{RetainedTurns: 10}
	for _, r := range []result{{turn: 3, generation: 1, phase: "context", context: &stale}, {turn: 2, generation: 2, phase: "context", context: &stale}} {
		if err := s.result(r); err != nil {
			t.Fatal(err)
		}
	}
	if len(snapshots) != 1 || s.state.Context != want {
		t.Fatal("stale context event replaced cleared context")
	}
}
