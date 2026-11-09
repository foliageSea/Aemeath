package voice

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A preview goes through the real session submit timeout as well as the worker.
// Inspecting its deadline reproduces the old 115s parent cap without waiting minutes.
func TestCloneSynthesisGetsFullBudgetAndReportsFailureElapsed(t *testing.T) {
	dep := fakeDependencies()
	checked := make(chan error, 1)
	dep.newSpeech = func(Config) speechAPI {
		return &fakeSpeech{synthesize: func(ctx context.Context, _ string, _ string) ([]int16, error) {
			deadline, ok := ctx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining < 179*time.Second || remaining > 181*time.Second {
				checked <- fmt.Errorf("TTS budget=%v deadline=%t", remaining, ok)
			} else {
				checked <- nil
			}
			timer := time.NewTimer(15 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil, errors.New("MiMo TTS: response interrupted; retry explicitly")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}
	}
	ready := make(chan struct{})
	finished := make(chan struct{})
	var commands chan Command
	commands, _, snapshots := sessionHarness(t, Config{Enabled: true, MiMoAPIKey: "secret"}, dep, func(d testDevice) error {
		<-ready
		commands <- Command{Kind: "preview", Text: "你好。"}
		if err := d.untilPhase("synthesizing"); err != nil {
			return err
		}
		if _, err := d.until("error"); err != nil {
			return err
		}
		if err := d.untilPhase("error"); err != nil {
			return err
		}
		close(finished)
		return nil
	}, cloudWorkBudget+10*time.Second)
	close(ready)
	select {
	case err := <-checked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no synthesis request")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("failure not surfaced")
	}
	found := false
	for _, s := range snapshots() {
		if s.Phase == "error" && s.TTSMillis >= 10 && s.Response == "你好。" {
			found = true
		}
	}
	if !found {
		t.Fatal("synthesis failure did not retain elapsed time")
	}
}

func TestCloudWorkerBudgetCoversAllStages(t *testing.T) {
	if cloudWorkBudget < recognitionBudget+agentBudget+synthesisBudget {
		t.Fatal("whole worker cuts a later stage short")
	}
	for _, phase := range []string{"recognizing", "thinking", "synthesizing"} {
		if phaseBudget(phase) <= 0 {
			t.Fatalf("missing phase budget: %s", phase)
		}
	}
}
