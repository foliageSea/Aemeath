package voice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkerDeferredBrainAndHistoryClear(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dep := fakeDependencies()
	engine := &fakeBrain{}
	created := 0
	dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { created++; return engine, nil }
	jobs := make(chan work, 1)
	out := make(chan result, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker(ctx, Config{MiMoAPIKey: "mimo", LLMAPIKey: "llm", LLMModel: "model"}, nil, dep, jobs, out)
	}()
	consume := func(turn uint32) {
		t.Helper()
		for {
			select {
			case r := <-out:
				if r.turn != turn {
					t.Fatalf("unexpected turn %d", r.turn)
				}
				if r.err != nil {
					t.Fatal(r.err)
				}
				if r.phase == "audio" || r.phase == "generation_done" {
					return
				}
			case <-time.After(time.Second):
				t.Fatal("worker stalled")
			}
		}
	}
	jobs <- work{ctx: ctx, turn: 1, kind: "preview", text: "测试"}
	consume(1)
	if created != 0 {
		t.Fatal("preview initialized brain")
	}
	jobs <- work{ctx: ctx, turn: 2, kind: "utterance", pcm: []int16{1}}
	consume(2)
	if created != 1 {
		t.Fatal("brain not lazy initialized once")
	}
	// A newer generation must clear before its first reply, even when an explicit
	// queued clear command was replaced by the next utterance.
	jobs <- work{ctx: ctx, turn: 3, generation: 2, kind: "utterance", pcm: []int16{1}}
	consume(3)
	if created != 1 || engine.clears.Load() != 1 {
		t.Fatalf("history generation not cleared: created=%d clears=%d", created, engine.clears.Load())
	}
	cancel()
	<-done
}

func TestManualCaptureAvailableWithoutWakeAssetsAndRedactsErrors(t *testing.T) {
	dep := fakeDependencies()
	dep.newDetector = func(Config) (detector, error) { return nil, errors.New("missing models") }
	dep.newVAD = func(Config) (vad, error) { return nil, errors.New("missing VAD") }
	pcmSeen := make(chan int, 1)
	dep.newSpeech = func(Config) speechAPI {
		return &fakeSpeech{recognize: func(_ context.Context, p []int16) (string, error) {
			pcmSeen <- len(p)
			return "", errors.New("provider said private-mimo-key")
		}}
	}
	ready := make(chan struct{})
	finished := make(chan struct{})
	var cmds chan Command
	cmds, _, snapshots := sessionHarness(t, Config{Enabled: true, MiMoAPIKey: "private-mimo-key"}, dep, func(d testDevice) error {
		<-ready
		cmds <- Command{Kind: "wake"}
		if e := d.untilPhase("listening"); e != nil {
			return e
		}
		for i := uint32(0); i < 300; i++ {
			if e := d.mic(i, 1); e != nil {
				return e
			}
		}
		m, e := d.until("error")
		if e != nil {
			return e
		}
		if strings.Contains(m.Message, "private-mimo-key") || !strings.Contains(m.Message, "[redacted]") {
			return errors.New("wire leaked credentials")
		}
		if e = d.untilPhase("error"); e != nil {
			return e
		}
		close(finished)
		return nil
	})
	close(ready)
	select {
	case n := <-pcmSeen:
		if n != manualWindowSamples {
			t.Fatalf("manual capture length=%d", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual capture did not reach ASR")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("manual capture did not complete")
	}
	diagnosed := false
	for _, s := range snapshots() {
		if s.WakeReady {
			t.Fatal("missing models reported ready")
		}
		if strings.Contains(s.Error, "private-mimo-key") {
			t.Fatal("snapshot leaked key")
		}
		if strings.Contains(s.Error, "fixed 6 second") {
			diagnosed = true
		}
	}
	if !diagnosed {
		t.Fatal("manual fixed window not diagnosed")
	}
}

func TestInvalidPlaybackAcknowledgementTerminatesTurn(t *testing.T) {
	ready := make(chan struct{})
	finished := make(chan struct{})
	var cmds chan Command
	cmds, _, _ = sessionHarness(t, Config{Enabled: true}, fakeDependencies(), func(d testDevice) error {
		<-ready
		cmds <- Command{Kind: "reference"}
		m, e := d.until("playback_start")
		if e != nil {
			return e
		}
		// No credit and no rendered audio: played must never silently finish the turn.
		if e = d.send(message{Type: "played", Turn: m.Turn}); e != nil {
			return e
		}
		m, e = d.until("error")
		if e != nil {
			return e
		}
		if m.Code != "bad_played" {
			return errors.New("accepted premature ACK")
		}
		if e = d.untilPhase("error"); e != nil {
			return e
		}
		close(finished)
		return nil
	})
	close(ready)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("invalid ACK not rejected")
	}
}
