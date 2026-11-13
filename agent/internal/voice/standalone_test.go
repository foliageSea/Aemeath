package voice

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aemeath/agent/internal/audioinput"
)

// Fires once, including across the capture reset caused by a successful wake.
type localWakeDetector struct {
	fakeDetector
	fired bool
}

func (d *localWakeDetector) Feed(p []int16) (bool, error) {
	hit, err := d.fakeDetector.Feed(p)
	if hit && !d.fired {
		d.fired = true
		return true, err
	}
	return false, err
}

type fakeLocalInput struct {
	frames chan audioinput.Frame
	closed atomic.Bool
}

func (i *fakeLocalInput) Frames() <-chan audioinput.Frame { return i.frames }
func (i *fakeLocalInput) Close()                          { i.closed.Store(true) }

type localHarness struct {
	ctx      context.Context
	cancel   context.CancelFunc
	input    *fakeLocalInput
	commands chan Command
	states   chan State
	done     chan error
}

func standaloneHarness(t *testing.T, cfg Config, dep dependencies) *localHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	h := &localHarness{ctx: ctx, cancel: cancel, input: &fakeLocalInput{frames: make(chan audioinput.Frame)}, commands: make(chan Command, 8), states: make(chan State, 256), done: make(chan error, 1)}
	dep.newInput = func(context.Context) (audioinput.Input, error) { return h.input, nil }
	go func() {
		h.done <- runStandaloneWithDependencies(ctx, cfg, nil, h.commands, func(s State) { h.states <- s }, dep)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-h.done:
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("session shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("standalone shutdown stalled")
		}
		if !h.input.closed.Load() {
			t.Error("microphone not closed")
		}
	})
	h.until(t, func(s State) bool { return s.Connected && s.Phase == "idle" })
	return h
}
func (h *localHarness) until(t *testing.T, match func(State) bool) State {
	t.Helper()
	for {
		select {
		case s := <-h.states:
			if match(s) {
				return s
			}
		case <-h.ctx.Done():
			t.Fatal("timed out waiting for standalone state")
			return State{}
		}
	}
}
func (h *localHarness) feed(t *testing.T, value int16, frames int) {
	t.Helper()
	for range frames {
		pcm := make([]int16, frameSamples)
		for j := range pcm {
			pcm[j] = value
		}
		select {
		case h.input.frames <- audioinput.Frame{PCM: pcm}:
		case <-h.ctx.Done():
			t.Fatal("PCM receiver stalled")
		}
	}
}

func TestStandaloneSpeechPathsUseLocalOutput(t *testing.T) {
	for _, kind := range []string{"reference", "preview", "text", "hold", "wake", "keyword", "fish"} {
		t.Run(kind, func(t *testing.T) {
			dep := fakeDependencies()
			output := &fakeConsoleOutput{}
			output.allowDrain.Store(true)
			dep.newOutput = func(c Config) (consoleOutput, error) {
				if c.PlaybackOutput != "console" || c.Volume != 63 {
					t.Errorf("effective local route: %+v", c.PlaybackOutput)
				}
				return output, nil
			}
			cfg := Config{Enabled: true, PlaybackOutput: "agent", MiMoAPIKey: "fake", LLMModel: "fake", Volume: 63}
			recognized := make(chan []int16, 1)
			dep.newSpeech = func(Config) speechAPI {
				return &fakeSpeech{recognize: func(_ context.Context, pcm []int16) (string, error) {
					recognized <- append([]int16(nil), pcm...)
					return "现在几点", nil
				}}
			}
			if kind == "keyword" {
				dep.newDetector = func(Config) (detector, error) {
					return &localWakeDetector{fakeDetector: fakeDetector{hitAt: frameSamples, end: frameSamples}}, nil
				}
			}
			if kind == "fish" {
				cfg.TTSProvider = "fish"
				cfg.FishAPIKey = "fake"
				cfg.FishReferenceID = "id"
				dep.newSpeech = func(Config) speechAPI {
					return &streamTestSpeech{stream: func(ctx context.Context, in <-chan string, audio func([]int16) error) error {
						<-in
						return audio(make([]int16, 641))
					}}
				}
			}
			h := standaloneHarness(t, cfg, dep)
			switch kind {
			case "hold":
				h.commands <- Command{Kind: "record_start"}
				h.until(t, func(s State) bool { return s.Phase == "listening" })
				h.feed(t, 7, 3)
				h.commands <- Command{Kind: "record_stop"}
			case "wake", "keyword":
				if kind == "wake" {
					h.commands <- Command{Kind: "wake"}
				} else {
					h.feed(t, 9, 1)
				}
				h.until(t, func(s State) bool { return s.Phase == "listening" })
				h.feed(t, 7, 3)
				h.feed(t, 0, endSilenceSamples/frameSamples)
			case "fish":
				h.commands <- Command{Kind: "preview", Text: "你好"}
			default:
				h.commands <- Command{Kind: kind, Text: "你好"}
			}
			final := h.until(t, func(s State) bool { return s.Turn == 1 && s.Phase == "idle" && s.PlayedSegments == 1 })
			if final.Error != "" || final.GeneratedSegments != 1 {
				t.Fatalf("unexpected result: %+v", final)
			}
			if cfg.PlaybackOutput != "agent" {
				t.Fatal("saved configuration changed")
			}
			pcm, finished, closed := output.snapshot()
			if len(pcm) != 641 || !finished || !closed {
				t.Fatalf("local playback incomplete: %d %v %v", len(pcm), finished, closed)
			}
			if kind == "hold" || kind == "wake" || kind == "keyword" {
				select {
				case samples := <-recognized:
					if len(samples) < 3*frameSamples || samples[0] != 7 {
						t.Fatalf("captured PCM=%d first=%d", len(samples), samples[0])
					}
				default:
					t.Fatal("capture never reached ASR")
				}
			}
		})
	}
}

func TestStandaloneInputFailureCancelsWorkerAndClosesDevices(t *testing.T) {
	dep := fakeDependencies()
	input := &fakeLocalInput{frames: make(chan audioinput.Frame, 1)}
	output := &fakeConsoleOutput{}
	dep.newInput = func(context.Context) (audioinput.Input, error) { return input, nil }
	dep.newOutput = func(Config) (consoleOutput, error) { return output, nil }
	workerExited := make(chan struct{})
	dep.newSpeech = func(Config) speechAPI {
		return &streamTestSpeech{stream: func(ctx context.Context, in <-chan string, audio func([]int16) error) error {
			defer close(workerExited)
			<-in
			if err := audio(make([]int16, frameSamples)); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	commands := make(chan Command, 1)
	commands <- Command{Kind: "preview"}
	done := make(chan error, 1)
	var final State
	go func() {
		done <- runStandaloneWithDependencies(ctx, fishStreamTestConfig(), nil, commands, func(s State) {
			final = s
			if s.Phase == "speaking" {
				input.frames <- audioinput.Frame{Err: errors.New("microphone unplugged")}
			}
		}, dep)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unplugged") {
			t.Fatalf("lost input error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("input failure stalled")
	}
	if final.Connected || final.ErrorStage != "listening" || !input.closed.Load() {
		t.Fatalf("input failure state: %+v", final)
	}
	select {
	case <-workerExited:
	default:
		t.Fatal("cloud worker outlived microphone failure")
	}
	_, _, closed := output.snapshot()
	if !closed {
		t.Fatal("output outlived input failure")
	}
}

func TestStandaloneDisabledAndPermissionFailureDoNotStartCloud(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		dep := fakeDependencies()
		var opened bool
		dep.newInput = func(context.Context) (audioinput.Input, error) {
			opened = true
			return nil, errors.New("microphone permission denied")
		}
		dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) {
			t.Fatal("unexpected cloud call")
			return nil, nil
		}
		var final State
		err := runStandaloneWithDependencies(context.Background(), Config{Enabled: enabled}, nil, nil, func(s State) { final = s }, dep)
		if opened != enabled || final.Connected {
			t.Fatalf("opened=%v state=%+v", opened, final)
		}
		if enabled {
			if err == nil || final.ErrorStage != "listening" || final.Phase != "error" {
				t.Fatalf("permission diagnostic: %v %+v", err, final)
			}
		} else if err != nil || final.Phase != "disabled" {
			t.Fatalf("disabled: %v %+v", err, final)
		}
	}
}

func TestStandalonePlaybackCannotWakeItselfAndManualHoldInterrupts(t *testing.T) {
	dep := fakeDependencies()
	output := &fakeConsoleOutput{}
	dep.newOutput = func(Config) (consoleOutput, error) { return output, nil }
	dep.newDetector = func(Config) (detector, error) { return &fakeDetector{hitAt: frameSamples, end: frameSamples}, nil }
	h := standaloneHarness(t, Config{Enabled: true}, dep)
	h.commands <- Command{Kind: "reference"}
	h.until(t, func(s State) bool { return s.Phase == "speaking" })
	h.feed(t, 9, 3)
	// A command round trip proves preceding PCM was consumed while still playing.
	h.commands <- Command{Kind: "record_start"}
	state := h.until(t, func(s State) bool { return s.Phase == "listening" })
	if state.Turn != 2 {
		t.Fatalf("speaker triggered an automatic turn: %+v", state)
	}
	_, _, closed := output.snapshot()
	if !closed {
		t.Fatal("manual hold did not stop playback")
	}
	h.feed(t, 7, 1)
	h.commands <- Command{Kind: "record_cancel"}
	h.until(t, func(s State) bool { return s.Phase == "idle" && s.Turn == 2 })
	// Reset after playback restores automatic keyword spotting.
	h.feed(t, 9, 1)
	h.until(t, func(s State) bool { return s.Phase == "listening" && s.Turn == 3 })
}
