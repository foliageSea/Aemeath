package voice

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWakeOnlyRepromptsAndBoundsFurtherCapture(t *testing.T) {
	for _, follow := range []string{"question", "silence", "wake_again", "cancel", "initial_silence"} {
		t.Run(follow, func(t *testing.T) {
			dep := fakeDependencies()
			dep.newDetector = func(Config) (detector, error) {
				end := int64(320)
				if follow == "initial_silence" {
					end = 640
				}
				return &fakeDetector{hitAt: 640, end: end}, nil
			}
			var asrCalls, brains, ttsCalls atomic.Int32
			dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { brains.Add(1); return &fakeBrain{}, nil }
			dep.newSpeech = func(Config) speechAPI {
				return &fakeSpeech{recognize: func(_ context.Context, pcm []int16) (string, error) {
					n := asrCalls.Add(1)
					if n == 1 || follow == "wake_again" {
						return "你好，小爱。", nil
					}
					for _, v := range pcm {
						if v == 7 || v == 99 {
							return "", fmt.Errorf("old wake samples leaked into second recording")
						}
					}
					return "现在几点了", nil
				}, synthesize: func(context.Context, string, string) ([]int16, error) {
					ttsCalls.Add(1)
					return make([]int16, 641), nil
				}}
			}
			finished := make(chan struct{})
			_, _, states := sessionHarness(t, Config{Enabled: true, MiMoAPIKey: "test-key", LLMModel: "model"}, dep, func(d testDevice) error {
				next := uint32(0)
				frame := func(v int16) error { err := d.mic(next, v); next++; return err }
				phrase := func(v int16) error {
					if err := frame(v); err != nil {
						return err
					}
					for i := 0; i < 40; i++ {
						if err := frame(0); err != nil {
							return err
						}
					}
					return nil
				}
				if err := frame(99); err != nil {
					return err
				}
				if err := frame(7); err != nil {
					return err
				}
				if err := d.untilPhase("wake"); err != nil {
					return err
				}
				if err := d.untilPhase("listening"); err != nil {
					return err
				}
				if follow == "initial_silence" {
					for i := 0; i < 250; i++ {
						if err := frame(0); err != nil {
							return err
						}
					}
					if err := d.untilPhase("idle"); err != nil {
						return err
					}
					close(finished)
					// Keep the socket alive until assertions finish; an early close
					// races the snapshot check with a legitimate disconnect error.
					<-d.ctx.Done()
					return nil
				}
				for i := 0; i < 40; i++ {
					if err := frame(0); err != nil {
						return err
					}
				}
				for {
					m, err := d.until("state")
					if err != nil {
						return err
					}
					if m.State == "error" {
						return fmt.Errorf("wake-only became error: %+v", m)
					}
					if m.State == "listening" && m.Text != "" {
						if m.Turn != 1 || !strings.Contains(m.Text, "继续说出问题") {
							return fmt.Errorf("bad reprompt: %+v", m)
						}
						break
					}
				}
				switch follow {
				case "question":
					if err := phrase(8); err != nil {
						return err
					}
					tr, err := d.until("transcript")
					if err != nil {
						return err
					}
					if tr.Turn != 1 || tr.Text != "现在几点了" {
						return fmt.Errorf("bad continued transcript: %+v", tr)
					}
					if _, err = d.until("response"); err != nil {
						return err
					}
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
					if err = d.send(message{Type: "played", Turn: start.Turn}); err != nil {
						return err
					}
				case "silence":
					for i := 0; i < 250; i++ {
						if err := frame(0); err != nil {
							return err
						}
					}
				case "wake_again":
					if err := phrase(7); err != nil {
						return err
					}
				case "cancel":
					if err := d.send(message{Type: "cancel", Turn: 1}); err != nil {
						return err
					}
				}
				if err := d.untilPhase("idle"); err != nil {
					return err
				}
				close(finished)
				<-d.ctx.Done() // assertions inspect the live session, not disconnection
				return nil
			})
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("wake-only recovery stalled")
			}
			wantASR := int32(1)
			if follow == "initial_silence" {
				wantASR = 0
			}
			if follow == "question" || follow == "wake_again" {
				wantASR = 2
			}
			if asrCalls.Load() != wantASR {
				t.Fatalf("ASR calls=%d want=%d", asrCalls.Load(), wantASR)
			}
			wantGeneration := int32(0)
			if follow == "question" {
				wantGeneration = 1
			}
			if brains.Load() != wantGeneration || ttsCalls.Load() != wantGeneration {
				t.Fatalf("unexpected generation: brains=%d tts=%d", brains.Load(), ttsCalls.Load())
			}
			sawNotice := false
			for _, s := range states() {
				if s.Phase == "error" || s.Error != "" {
					t.Fatalf("normal wake became error: %+v", s)
				}
				if s.Phase == "listening" && s.Notice != "" {
					sawNotice = true
				}
				if s.Turn > 1 {
					t.Fatal("reprompt created extra turn")
				}
			}
			if !sawNotice && follow != "initial_silence" {
				t.Fatal("missing ordinary listening prompt")
			}
		})
	}
}
