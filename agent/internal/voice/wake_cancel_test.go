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

func TestWakeKeywordRestartsEveryActiveStageWithWakeCue(t *testing.T) {
	for _, phase := range []string{"listening", "recognizing", "thinking", "synthesizing", "speaking"} {
		t.Run(phase, func(t *testing.T) {
			s := toolTestSession(t)
			s.epoch = 42
			s.wireTurn, s.firstWireTurn = 1, 1
			s.state.Phase = phase
			s.state.WakeReady = true
			s.state.Transcript = "原问题"
			s.state.Response = "已有回复"
			oldCalls := []brain.ToolCall{{ID: 1, Name: "web_search", Status: "running"}}
			s.state.ToolCalls = oldCalls
			hitAt := 3 * frameSamples
			s.detector = &fakeDetector{hitAt: int64(hitAt), end: frameSamples}
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
			// The question spoken immediately after the keyword is discarded.
			recordFrame(t, s, 99)
			recordFrame(t, s, 7)
			if !s.active || s.state.Phase != "wake" || s.state.Turn != 2 || workCtx.Err() != context.Canceled {
				t.Fatalf("turn not restarted: active=%v turn=%d phase=%s context=%v", s.active, s.state.Turn, s.state.Phase, workCtx.Err())
			}
			if s.capture != nil || !s.wakeCue || s.play == nil || len(s.play.pcm) != len(wakeCuePCM) || len(s.queuedAudio) != 0 || s.deadline.IsZero() || !s.workDeadline.IsZero() || s.cancelTurn != nil {
				t.Fatal("restart retained old playback/work or did not start the wake cue")
			}
			if oldCalls[0].Status != "canceled" || len(s.state.ToolCalls) != 0 || s.state.Transcript != "" || s.state.Response != "" {
				t.Fatal("restart did not settle old tools and clear turn state")
			}
			noRecordWork(t, s)
			// Further keyword hits are not under test after the restart.
			s.detector = &fakeDetector{}
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
			if !reflect.DeepEqual(before, s.state) || !s.wakeCue || len(s.queuedAudio) != 0 || s.capture != nil {
				t.Fatal("late old-turn result or device event affected the new turn")
			}
			// Speech during the cue is never captured.
			recordFrame(t, s, 6)
			completeWakeCueDirect(t, s)
			if len(s.capture.pcm) != 0 {
				t.Fatal("audio before the cue finished leaked into capture")
			}
			for range cueTailFrames {
				recordFrame(t, s, 5)
			}
			if len(s.capture.pcm) != 0 || s.capture.speech {
				t.Fatal("cue echo tail reached capture")
			}
			recordFrame(t, s, 8)
			for i := 0; i < endSilenceSamples/frameSamples; i++ {
				recordFrame(t, s, 0)
			}
			w := nextRecordWork(t, s)
			defer s.cancelTurn()
			if w.turn != 2 || w.kind != "utterance" || w.ctx.Err() != nil || len(w.pcm) != frameSamples+endSilenceSamples {
				t.Fatalf("bad new question work: turn=%d kind=%s samples=%d context=%v", w.turn, w.kind, len(w.pcm), w.ctx.Err())
			}
			for i, sample := range w.pcm {
				want := int16(0)
				if i < frameSamples {
					want = 8
				}
				if sample != want {
					t.Fatalf("ASR audio[%d]=%d want=%d", i, sample, want)
				}
			}
		})
	}
}

func TestWakeKeywordDiscardsBufferedAudio(t *testing.T) {
	for _, end := range []int64{0, -1, frameSamples / 2} {
		t.Run(fmt.Sprint(end), func(t *testing.T) {
			s := recordTestSession(t)
			s.state.WakeReady = true
			s.detector = &fakeDetector{hitAt: frameSamples, end: end}
			s.vad = &fakeVAD{}
			recordFrame(t, s, 99)
			if s.state.Turn != 2 || s.state.Phase != "wake" || s.capture != nil || !s.wakeCue {
				t.Fatal("keyword did not restart into the wake cue")
			}
		})
	}
}

func TestWakeKeywordDuringPlaybackPlaysCueBeforeQuestion(t *testing.T) {
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
		// Audio before and while the cue plays is not part of the question.
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
		cueTurn, err := d.completeWakeCue()
		if err != nil {
			return err
		}
		seq, err := d.micFrames(3, 0, cueTailFrames)
		if err != nil {
			return err
		}
		if seq, err = d.micFrames(seq, 7, 1); err != nil {
			return err
		}
		if seq, err = d.micFrames(seq, 8, 1); err != nil {
			return err
		}
		if _, err = d.micFrames(seq, 0, endSilenceSamples/frameSamples); err != nil {
			return err
		}
		transcript, err := d.until("transcript")
		if err != nil {
			return err
		}
		if transcript.Text != "新问题" || transcript.Turn != cueTurn+1 {
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
