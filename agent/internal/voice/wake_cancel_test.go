package voice

import (
	"context"
	"fmt"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
)

func TestWakeKeywordCancelsEveryActiveStage(t *testing.T) {
	for _, phase := range []string{"listening", "recognizing", "thinking", "synthesizing", "speaking"} {
		t.Run(phase, func(t *testing.T) {
			s := toolTestSession(t)
			s.epoch = 42
			s.wireTurn = 1
			s.state.Phase = phase
			s.state.WakeReady = true
			s.state.Transcript = "原问题"
			s.state.Response = "已有回复"
			s.state.ToolCalls = []brain.ToolCall{{ID: 1, Name: "web_search", Status: "running"}}
			s.detector = &fakeDetector{hitAt: frameSamples}
			s.vad = &fakeVAD{}
			if phase == "listening" {
				s.capture = &capture{}
			}
			s.play = &playback{pcm: []int16{1}, streaming: phase == "speaking"}
			s.queuedAudio = []speechClip{{pcm: []int16{2}}}
			workCtx, cancel := context.WithCancel(s.ctx)
			defer cancel()
			s.cancelTurn = cancel
			s.deadline = time.Now().Add(time.Minute)
			s.workDeadline = s.deadline
			frame := func(seq uint32, value int16) []byte {
				pcm := make([]int16, frameSamples)
				pcm[0] = value
				return encodeAudio(audioFrame{Kind: kindMicrophone, Epoch: 42, Seq: seq, PCM: pcm})
			}
			// Ordinary audio must neither cancel cloud work nor interrupt playback.
			if err := s.microphone(frame(0, 0)); err != nil {
				t.Fatal(err)
			}
			if !s.active || workCtx.Err() != nil {
				t.Fatal("ordinary audio canceled the turn")
			}
			// Align the fake detection to the next frame, after testing a non-match.
			s.detector.(*fakeDetector).hitAt = 2 * frameSamples
			if err := s.microphone(frame(1, 99)); err != nil {
				t.Fatal(err)
			}
			if s.active || s.state.Phase != "idle" || workCtx.Err() != context.Canceled {
				t.Fatalf("turn not canceled: active=%v phase=%s context=%v", s.active, s.state.Phase, workCtx.Err())
			}
			if s.capture != nil || s.play != nil || len(s.queuedAudio) != 0 || !s.deadline.IsZero() || !s.workDeadline.IsZero() {
				t.Fatal("canceled turn retained capture, playback, or deadlines")
			}
			if s.state.ToolCalls[0].Status != "canceled" || s.state.Transcript != "原问题" || s.state.Response != "已有回复" {
				t.Fatal("cancel lost conversation or left tool running")
			}
			if s.state.Turn != 1 || len(s.jobs) != 0 {
				t.Fatal("cancellation keyword started another turn or ASR job")
			}
			if err := s.result(result{turn: 1, generation: s.generation, phase: "audio_segment", pcm: []int16{3}}); err != nil {
				t.Fatal(err)
			}
			if err := s.result(result{turn: 1, generation: s.generation, phase: "response", text: "迟到的回复"}); err != nil {
				t.Fatal(err)
			}
			if s.play != nil || len(s.queuedAudio) != 0 || s.state.Response != "已有回复" {
				t.Fatal("late worker result revived canceled turn")
			}
		})
	}
}

func TestWakeKeywordDuringPlaybackSendsCancelAndCanWakeAgain(t *testing.T) {
	dep := fakeDependencies()
	dep.newDetector = func(c Config) (detector, error) {
		if c.WakePhrase != "爱弥斯" {
			return nil, fmt.Errorf("custom wake phrase not applied: %s", c.WakePhrase)
		}
		return &fakeDetector{hitAt: frameSamples, end: frameSamples}, nil
	}
	ready, finished := make(chan struct{}), make(chan struct{})
	var commands chan Command
	commands, _, _ = sessionHarness(t, Config{Enabled: true, Volume: 70, WakePhrase: "爱弥斯"}, dep, func(d testDevice) error {
		<-ready
		commands <- Command{Kind: "reference"}
		start, err := d.until("playback_start")
		if err != nil {
			return err
		}
		if err = d.send(message{Type: "credit", Turn: start.Turn, Frames: 32}); err != nil {
			return err
		}
		if _, err = d.until("playback_end"); err != nil {
			return err
		}
		if err = d.send(message{Type: "playing", Turn: start.Turn}); err != nil {
			return err
		}
		if err = d.untilPhase("speaking"); err != nil {
			return err
		}
		if err = d.mic(0, 99); err != nil {
			return err
		}
		canceled, err := d.until("cancel")
		if err != nil {
			return err
		}
		if canceled.Turn != start.Turn {
			return fmt.Errorf("cancel turn=%d want=%d", canceled.Turn, start.Turn)
		}
		if err = d.untilPhase("idle"); err != nil {
			return err
		}
		// The next invocation from idle should still start a fresh recording.
		if err = d.mic(1, 99); err != nil {
			return err
		}
		if err = d.untilPhase("listening"); err != nil {
			return err
		}
		// Invoking once more while recording cancels that capture too.
		if err = d.mic(2, 99); err != nil {
			return err
		}
		if _, err = d.until("cancel"); err != nil {
			return err
		}
		if err = d.untilPhase("idle"); err != nil {
			return err
		}
		close(finished)
		return nil
	})
	close(ready)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("wake cancellation did not stop playback and recover")
	}
}

func TestMutedMicrophoneDoesNotCancelOnWakeKeyword(t *testing.T) {
	s := toolTestSession(t)
	s.epoch = 42
	s.muted = true
	s.state.WakeReady = true
	s.detector = &fakeDetector{hitAt: frameSamples}
	pcm := make([]int16, frameSamples)
	pcm[0] = 99
	if err := s.microphone(encodeAudio(audioFrame{Kind: kindMicrophone, Epoch: 42, PCM: pcm})); err != nil {
		t.Fatal(err)
	}
	if !s.active || s.detector.(*fakeDetector).samples != 0 {
		t.Fatal("muted microphone reached wake detector")
	}
}
