package voice

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRespeakOnlySynthesizesSavedReplyAndReusesCache(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "mimo", true: "fish"}[streaming], func(t *testing.T) {
			s, outputs := replayTestSession(t)
			ctx, cancel := context.WithCancel(context.Background())
			s.ctx = ctx
			s.results = make(chan result, 16)
			var modelCalls, asrCalls, ttsCalls atomic.Int32
			var spoken strings.Builder
			dep := fakeDependencies()
			dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) {
				modelCalls.Add(1)
				return nil, errors.New("must not call LLM")
			}
			speech := &fakeSpeech{
				recognize: func(context.Context, []int16) (string, error) {
					asrCalls.Add(1)
					return "", errors.New("must not call ASR")
				},
				synthesize: func(_ context.Context, text, _ string) ([]int16, error) {
					ttsCalls.Add(1)
					spoken.WriteString(text)
					return []int16{1, 2}, nil
				},
			}
			dep.newSpeech = func(Config) speechAPI {
				if !streaming {
					return speech
				}
				return &streamTestSpeech{fakeSpeech: *speech, stream: func(_ context.Context, parts <-chan string, audio func([]int16) error) error {
					ttsCalls.Add(1)
					for part := range parts {
						spoken.WriteString(part)
					}
					return audio([]int16{1, 2})
				}}
			}
			cfg := Config{MiMoAPIKey: "test"} // Deliberately no LLM model, credentials, or memory.
			if streaming {
				cfg = Config{TTSProvider: "fish", FishAPIKey: "test", FishReferenceID: "voice"}
			}
			done := make(chan struct{})
			go func() { defer close(done); worker(ctx, cfg, nil, dep, s.jobs, s.results) }()
			defer func() { cancel(); <-done }()
			text := "这是以前的回复。[1]\n[1] https://example.com/source"
			key := ReplayTextKey(text)
			if err := s.command(Command{Kind: "respeak", Text: " " + text + " "}); err != nil {
				t.Fatal(err)
			}
			if s.state.Transcript != "" || s.state.ReplayingKey != key || s.state.Phase != "synthesizing" {
				t.Fatalf("bad initial state: %+v", s.state)
			}
			deadline := time.After(3 * time.Second)
		collect:
			for {
				select {
				case r := <-s.results:
					if r.err != nil {
						t.Fatal(r.err)
					}
					if err := s.result(r); err != nil {
						t.Fatal(err)
					}
					if r.phase == "generation_done" {
						break collect
					}
				case <-deadline:
					t.Fatal("resynthesis stalled")
				}
			}
			if modelCalls.Load() != 0 || asrCalls.Load() != 0 || ttsCalls.Load() == 0 {
				t.Fatal("wrong cloud path")
			}
			if strings.Contains(spoken.String(), "example.com") || strings.Contains(spoken.String(), "[1]") {
				t.Fatal("citation read aloud:", spoken.String())
			}
			if !reflect.DeepEqual(s.state.ReplayKeys, []string{key}) {
				t.Fatalf("wrong cache keys: %v", s.state.ReplayKeys)
			}
			calls := ttsCalls.Load()
			// A repeated explicit request uses the freshly completed audio. It does
			// not enqueue work, so even an absent TTS configuration would be fine.
			if err := s.command(Command{Kind: "respeak", Text: text}); err != nil {
				t.Fatal(err)
			}
			if len(s.jobs) != 0 || ttsCalls.Load() != calls || s.state.ReplayingKey != key {
				t.Fatal("cache hit resynthesized")
			}
			if len(*outputs) < 2 {
				t.Fatal("cached replay did not open output")
			}
			pcm, _, _ := (*outputs)[len(*outputs)-1].snapshot()
			if !reflect.DeepEqual(pcm, []int16{1, 2}) {
				t.Fatal(pcm)
			}
			clone := s.state.Clone()
			clone.ReplayKeys[0] = "mutated"
			if s.state.ReplayKeys[0] != key {
				t.Fatal("cache key aliases snapshot")
			}
			if err := s.command(Command{Kind: "cancel"}); err != nil {
				t.Fatal(err)
			}
			if s.state.ReplayingKey != "" || len(s.state.ReplayKeys) != 1 {
				t.Fatal("cancel damaged key cache")
			}
			if err := s.command(Command{Kind: "clear"}); err != nil {
				t.Fatal(err)
			}
			if len(s.state.ReplayKeys) != 0 {
				t.Fatal("clear retained text cache")
			}
		})
	}
}

func TestRespeakInvalidTextDoesNotInterruptCurrentOperation(t *testing.T) {
	for _, text := range []string{"", " \n\t", strings.Repeat("a", MaxReplayTextBytes+1), string([]byte{0xff})} {
		s, _ := replayTestSession(t)
		turn := startReplayConversation(t, s)
		if err := s.command(Command{Kind: "respeak", Text: text}); err != nil {
			t.Fatal(err)
		}
		if s.state.Turn != turn || !s.active || s.state.Transcript != "问题" || len(s.jobs) != 0 {
			t.Fatal("invalid text interrupted conversation")
		}
	}
}

func TestRespeakLongTextUsesFullReplyBudget(t *testing.T) {
	s, _ := replayTestSession(t)
	text := strings.Repeat("a", MaxReplayTextBytes)
	if err := s.command(Command{Kind: "respeak", Text: text}); err != nil {
		t.Fatal(err)
	}
	w := nextRecordWork(t, s)
	if w.kind != "respeak" || w.text != text || !s.replayCapture {
		t.Fatal("long saved reply not submitted")
	}
	if ReplayTextKey(" "+text+" ") != ReplayTextKey(text) {
		t.Fatal("text keys ignore trim contract")
	}
}
