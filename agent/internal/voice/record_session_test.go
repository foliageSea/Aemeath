package voice

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func recordTestSession(t *testing.T) *session {
	t.Helper()
	s := toolTestSession(t)
	s.epoch = 42
	return s
}

func nextRecordWork(t *testing.T, s *session) work {
	t.Helper()
	select {
	case w := <-s.jobs:
		return w
	default:
		t.Fatal("recording did not submit cloud work")
		return work{}
	}
}

func recordCommand(t *testing.T, s *session, kind string) {
	t.Helper()
	if err := s.command(Command{Kind: kind}); err != nil {
		t.Fatal(err)
	}
}
func recordFrame(t *testing.T, s *session, value int16) {
	t.Helper()
	pcm := make([]int16, frameSamples)
	for i := range pcm {
		pcm[i] = value
	}
	if err := s.microphone(encodeAudio(audioFrame{Kind: kindMicrophone, Epoch: s.epoch, Seq: s.nextMic, PCM: pcm})); err != nil {
		t.Fatal(err)
	}
}
func recordControl(t *testing.T, s *session, m message) {
	t.Helper()
	m.V, m.Epoch = protocolVersion, s.epoch
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.control(b); err != nil {
		t.Fatal(err)
	}
}
func noRecordWork(t *testing.T, s *session) {
	t.Helper()
	if len(s.jobs) != 0 {
		t.Fatal("unexpected cloud work")
	}
}

func TestRecordHoldReleasePreservesSilenceAndSubmitsOnce(t *testing.T) {
	for _, models := range []bool{true, false} {
		t.Run(map[bool]string{true: "models_ready", false: "models_unavailable"}[models], func(t *testing.T) {
			s := recordTestSession(t)
			if models {
				// This detector would cancel on the first frame if hold capture fed KWS.
				s.detector = &fakeDetector{hitAt: frameSamples}
				s.vad = &fakeVAD{}
				s.state.WakeReady = true
			}
			recordCommand(t, s, "record_start")
			turn, deadline, capture := s.state.Turn, s.deadline, s.capture
			recordCommand(t, s, "record_start")
			if s.state.Turn != turn || s.capture != capture || s.deadline != deadline {
				t.Fatal("duplicate start reset hold")
			}
			// Six seconds of initial silence exceeds the old five-second speech timeout.
			for i := 0; i < 300; i++ {
				recordFrame(t, s, 0)
			}
			recordFrame(t, s, 7)
			// A full second of trailing silence exceeds VAD end silence.
			for i := 0; i < 50; i++ {
				recordFrame(t, s, 0)
			}
			noRecordWork(t, s)
			if !s.recording || s.state.Phase != "listening" {
				t.Fatal("hold ended before release")
			}
			recordCommand(t, s, "record_stop")
			if s.recording || s.capture != nil || s.state.Phase != "recognizing" {
				t.Fatal("release did not submit")
			}
			w := nextRecordWork(t, s)
			if s.cancelTurn != nil {
				defer s.cancelTurn()
			}
			if w.kind != "utterance" || len(w.pcm) != 351*frameSamples || w.pcm[300*frameSamples] != 7 {
				t.Fatalf("bad recording: kind=%s samples=%d", w.kind, len(w.pcm))
			}
			recordCommand(t, s, "record_stop")
			noRecordWork(t, s)
		})
	}
}

func TestRecordStartInterruptsAndRejectsStaleResults(t *testing.T) {
	s := recordTestSession(t)
	oldCtx, cancel := context.WithCancel(s.ctx)
	s.cancelTurn = cancel
	oldTurn := s.state.Turn
	s.play = &playback{playing: true}
	s.queuedAudio = []speechClip{{pcm: []int16{1}}}
	recordCommand(t, s, "record_start")
	if oldCtx.Err() != context.Canceled || s.play != nil || len(s.queuedAudio) != 0 || s.state.Turn == oldTurn {
		t.Fatal("start did not interrupt prior turn")
	}
	if err := s.result(result{turn: oldTurn, generation: s.generation, phase: "response", text: "stale"}); err != nil {
		t.Fatal(err)
	}
	if s.state.Response != "" {
		t.Fatal("old turn result leaked")
	}
	recordCommand(t, s, "record_cancel")
}

func TestRecordCancelMuteEmptyAndWakeOnlyDiscard(t *testing.T) {
	for _, kind := range []string{"record_cancel", "cancel", "mute", "empty", "wake_only"} {
		t.Run(kind, func(t *testing.T) {
			s := recordTestSession(t)
			recordCommand(t, s, "record_start")
			if kind != "empty" {
				recordFrame(t, s, 3)
			}
			switch kind {
			case "mute":
				recordControl(t, s, message{Type: "mute", Muted: true})
			case "empty":
				recordCommand(t, s, "record_stop")
			case "wake_only":
				recordCommand(t, s, "record_stop")
				w := nextRecordWork(t, s)
				if err := s.result(result{turn: w.turn, generation: w.generation, phase: "wake_only"}); err != nil {
					t.Fatal(err)
				}
			default:
				recordCommand(t, s, kind)
			}
			recordCommand(t, s, "record_stop")
			if s.active || s.capture != nil || s.recording || s.state.Phase != "idle" {
				t.Fatal("discard retained recording")
			}
			noRecordWork(t, s)
			if kind == "mute" {
				turn := s.state.Turn
				recordCommand(t, s, "record_start")
				recordFrame(t, s, 4)
				recordCommand(t, s, "record_stop")
				if s.active || s.state.Turn != turn {
					t.Fatal("muted recording started")
				}
				noRecordWork(t, s)
				recordControl(t, s, message{Type: "mute", Muted: false})
				recordCommand(t, s, "record_start")
				if !s.recording {
					t.Fatal("unmute did not restore recording")
				}
				recordCommand(t, s, "record_cancel")
			}
		})
	}
}

func TestRecordThirtySecondSampleAndWallClockCaps(t *testing.T) {
	for _, kind := range []string{"samples", "wall_clock", "stalled_empty"} {
		t.Run(kind, func(t *testing.T) {
			s := recordTestSession(t)
			recordCommand(t, s, "record_start")
			if kind == "samples" {
				for i := 0; i < maxUtteranceSamples/frameSamples; i++ {
					recordFrame(t, s, 0)
				}
			} else {
				if kind != "stalled_empty" {
					recordFrame(t, s, 2)
				}
				if err := s.recordingTimeout(s.deadline.Add(-time.Nanosecond)); err != nil {
					t.Fatal(err)
				}
				if !s.recording {
					t.Fatal("wall clock cap fired early")
				}
				if err := s.recordingTimeout(s.deadline); err != nil {
					t.Fatal(err)
				}
			}
			if s.recording || s.capture != nil {
				t.Fatal("cap retained capture")
			}
			if kind == "stalled_empty" {
				noRecordWork(t, s)
				return
			}
			w := nextRecordWork(t, s)
			if s.cancelTurn != nil {
				defer s.cancelTurn()
			}
			want := frameSamples
			if kind == "samples" {
				want = maxUtteranceSamples
			}
			if len(w.pcm) != want {
				t.Fatalf("samples=%d want=%d", len(w.pcm), want)
			}
			recordCommand(t, s, "record_stop")
			recordCommand(t, s, "record_cancel")
			if !s.active {
				t.Fatal("late recording cancellation interrupted submitted turn")
			}
			noRecordWork(t, s)
		})
	}
}

func TestRecordReleaseRunsExistingASRAgentPipeline(t *testing.T) {
	s := recordTestSession(t)
	s.cfg = Config{Enabled: true, MiMoAPIKey: "test-key", LLMModel: "model"}
	dep := fakeDependencies()
	recognized := make(chan []int16, 1)
	dep.newSpeech = func(Config) speechAPI {
		return &fakeSpeech{recognize: func(_ context.Context, pcm []int16) (string, error) { recognized <- pcm; return "几点了", nil }}
	}
	recordCommand(t, s, "record_start")
	recordFrame(t, s, 8)
	noRecordWork(t, s)
	recordCommand(t, s, "record_stop")
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	results := make(chan result, 16)
	done := make(chan struct{})
	go func() { defer close(done); worker(ctx, s.cfg, nil, dep, s.jobs, results) }()
	defer func() { cancel(); <-done }()
	transcript, response := false, false
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for !transcript || !response {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.phase == "transcript" {
				transcript = r.text == "几点了"
			}
			if r.phase == "response" {
				response = r.text == "现在八点。"
			}
		case <-timeout.C:
			t.Fatal("release did not complete ASR and Agent")
		}
	}
	pcm := <-recognized
	if len(pcm) != frameSamples || pcm[0] != 8 {
		t.Fatal("ASR received wrong capture")
	}
	recordCommand(t, s, "record_stop")
	noRecordWork(t, s)
}
