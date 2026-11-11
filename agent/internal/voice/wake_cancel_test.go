package voice

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"aemeath/agent/internal/brain"
)

func TestWakeKeywordRestartsEveryActiveStageWithQuestionSuffix(t *testing.T) {
	for _, phase := range []string{"listening", "recognizing", "thinking", "synthesizing", "speaking"} {
		for _, delay := range []int{2 * frameSamples, preRollSamples + frameSamples} {
			t.Run(fmt.Sprintf("%s/delay_%d", phase, delay), func(t *testing.T) {
				s := toolTestSession(t)
				s.epoch = 42
				s.wireTurn, s.firstWireTurn = 1, 1
				s.state.Phase = phase
				s.state.WakeReady = true
				s.state.Transcript = "原问题"
				s.state.Response = "已有回复"
				oldCalls := []brain.ToolCall{{ID: 1, Name: "web_search", Status: "running"}}
				s.state.ToolCalls = oldCalls
				// The endpoint is inside a frame; recognition arrives after question
				// audio has already passed through the old capture/cloud/playback.
				end := frameSamples + frameSamples/2
				hitAt := 2*frameSamples + delay
				s.detector = &fakeDetector{hitAt: int64(hitAt), end: int64(end)}
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
				recordFrame(t, s, 11)
				if !s.active || workCtx.Err() != nil || s.state.Turn != 1 {
					t.Fatal("ordinary audio interrupted the turn")
				}
				for offset := frameSamples; offset < hitAt; offset += frameSamples {
					pcm := make([]int16, frameSamples)
					for i := range pcm {
						pcm[i] = 7
						if offset+i < end {
							pcm[i] = 99
						}
					}
					if err := s.microphone(encodeAudio(audioFrame{Kind: kindMicrophone, Epoch: s.epoch, Seq: s.nextMic, PCM: pcm})); err != nil {
						t.Fatal(err)
					}
				}
				if !s.active || s.state.Phase != "listening" || s.state.Turn != 2 || workCtx.Err() != context.Canceled {
					t.Fatalf("turn not restarted: active=%v turn=%d phase=%s context=%v", s.active, s.state.Turn, s.state.Phase, workCtx.Err())
				}
				if s.capture == nil || s.play != nil || len(s.queuedAudio) != 0 || s.deadline.IsZero() || !s.workDeadline.IsZero() || s.cancelTurn != nil {
					t.Fatal("restart retained old playback/work or lost new capture deadline")
				}
				if oldCalls[0].Status != "canceled" || len(s.state.ToolCalls) != 0 || s.state.Transcript != "" || s.state.Response != "" {
					t.Fatal("restart did not settle old tools and clear turn state")
				}
				wantSuffix := min(hitAt-end, preRollSamples)
				if len(s.capture.pcm) != wantSuffix || s.capture.elapsed != wantSuffix {
					t.Fatalf("suffix length=%d elapsed=%d want=%d", len(s.capture.pcm), s.capture.elapsed, wantSuffix)
				}
				for _, sample := range s.capture.pcm {
					if sample != 7 {
						t.Fatalf("old audio or keyword leaked into new capture: %d", sample)
					}
				}
				noRecordWork(t, s)
				before := s.state.Clone()
				for _, late := range []result{
					{phase: "audio_segment", pcm: []int16{3}},
					{phase: "audio_stream_start"},
					{phase: "audio_stream_chunk", pcm: []int16{3}},
					{phase: "transcript", text: "迟到的问题"},
					{phase: "response", text: "迟到的回复"},
					{phase: "thinking", err: errors.New("old failure")},
					{phase: "wake_only"},
					{phase: "generation_done"},
					{phase: "context", context: &brain.ContextStats{}},
					{toolCall: &brain.ToolCall{ID: 2, Name: "web_search", Status: "running"}},
				} {
					late.turn, late.generation = 1, s.generation
					if err := s.result(late); err != nil {
						t.Fatal(err)
					}
				}
				for _, kind := range []string{"cancel", "played", "error"} {
					recordControl(t, s, message{Type: kind, Turn: 1})
				}
				if !reflect.DeepEqual(before, s.state) || s.play != nil || len(s.queuedAudio) != 0 || len(s.capture.pcm) != wantSuffix {
					t.Fatal("late old-turn result or device event affected the new turn")
				}
				recordFrame(t, s, 8)
				for i := 0; i < endSilenceSamples/frameSamples; i++ {
					recordFrame(t, s, 0)
				}
				w := nextRecordWork(t, s)
				defer s.cancelTurn()
				if w.turn != 2 || w.kind != "utterance" || w.ctx.Err() != nil || len(w.pcm) != wantSuffix+frameSamples+endSilenceSamples {
					t.Fatalf("bad new question work: turn=%d kind=%s samples=%d context=%v", w.turn, w.kind, len(w.pcm), w.ctx.Err())
				}
				for i, sample := range w.pcm {
					want := int16(0)
					if i < wantSuffix {
						want = 7
					} else if i < wantSuffix+frameSamples {
						want = 8
					}
					if sample != want {
						t.Fatalf("ASR audio[%d]=%d want=%d", i, sample, want)
					}
				}
			})
		}
	}
}

func TestWakeKeywordInvalidEndpointExcludesBufferedAudio(t *testing.T) {
	for _, end := range []int64{0, -1, 2 * frameSamples} {
		t.Run(fmt.Sprint(end), func(t *testing.T) {
			s := recordTestSession(t)
			s.state.WakeReady = true
			s.detector = &fakeDetector{hitAt: frameSamples, end: end}
			s.vad = &fakeVAD{}
			recordFrame(t, s, 99)
			if s.state.Turn != 2 || s.capture == nil || len(s.capture.pcm) != 0 {
				t.Fatal("invalid endpoint included keyword audio or failed to restart")
			}
		})
	}
}

func TestWakeKeywordDuringPlaybackAcceptsContinuousQuestion(t *testing.T) {
	dep := fakeDependencies()
	dep.newDetector = func(c Config) (detector, error) {
		if c.WakePhrase != "爱弥斯" {
			return nil, fmt.Errorf("custom wake phrase not applied: %s", c.WakePhrase)
		}
		return &fakeDetector{hitAt: 3 * frameSamples, end: frameSamples}, nil
	}
	captured := make(chan []int16, 1)
	dep.newSpeech = func(Config) speechAPI {
		return &fakeSpeech{recognize: func(_ context.Context, pcm []int16) (string, error) {
			captured <- append([]int16(nil), pcm...)
			return "新问题", nil
		}}
	}
	ready, finished := make(chan struct{}), make(chan struct{})
	var commands chan Command
	commands, _, states := sessionHarness(t, Config{Enabled: true, Volume: 70, WakePhrase: "爱弥斯", MiMoAPIKey: "test-key", LLMModel: "model"}, dep, func(d testDevice) error {
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
		// The complete question arrives before the delayed KWS decision.
		for seq, value := range []int16{99, 7, 8} {
			if err = d.mic(uint32(seq), value); err != nil {
				return err
			}
		}
		canceled, err := d.until("cancel")
		if err != nil {
			return err
		}
		if canceled.Turn != start.Turn {
			return fmt.Errorf("cancel turn=%d want=%d", canceled.Turn, start.Turn)
		}
		wake, err := d.until("state")
		if err != nil {
			return err
		}
		if wake.State != "wake" || wake.Turn <= start.Turn {
			return fmt.Errorf("expected immediate fresh wake, got %+v", wake)
		}
		if err = d.untilPhase("listening"); err != nil {
			return err
		}
		for seq := uint32(3); seq < 3+endSilenceSamples/frameSamples; seq++ {
			if err = d.mic(seq, 0); err != nil {
				return err
			}
		}
		transcript, err := d.until("transcript")
		if err != nil {
			return err
		}
		if transcript.Text != "新问题" || transcript.Turn != wake.Turn {
			return fmt.Errorf("new question not recognized on new turn: %+v", transcript)
		}
		if _, err = d.until("playback_start"); err != nil {
			return err
		}
		// Explicit cancellation still cancels, without opening another capture.
		commands <- Command{Kind: "cancel"}
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
	case pcm := <-captured:
		if len(pcm) != 2*frameSamples+endSilenceSamples {
			t.Fatalf("ASR samples=%d", len(pcm))
		}
		for i, sample := range pcm {
			want := int16(0)
			if i < frameSamples {
				want = 7
			} else if i < 2*frameSamples {
				want = 8
			}
			if sample != want {
				t.Fatalf("ASR audio[%d]=%d want=%d", i, sample, want)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("new question did not reach ASR")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("wake interruption did not stop playback and accept the question")
	}
	for _, state := range states() {
		if state.Turn > 2 {
			t.Fatal("explicit cancel started another turn")
		}
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
