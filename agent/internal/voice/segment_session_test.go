package voice

import (
	"context"
	"fmt"
	"github.com/coder/websocket"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type sessionStreamingBrain struct {
	release       <-chan struct{}
	exited        chan struct{}
	endAfterFirst bool
}

func (b *sessionStreamingBrain) Clear() {}
func (b *sessionStreamingBrain) Reply(context.Context, string) (string, error) {
	return "", fmt.Errorf("non-streaming path unexpectedly used")
}
func (b *sessionStreamingBrain) ReplyStream(ctx context.Context, _ string, delta func(string) error) (string, error) {
	defer close(b.exited)
	first := "你好，我先为你报出当前的运行情况。"
	if err := delta(first); err != nil {
		return "", err
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if b.endAfterFirst {
		return first, nil
	}
	last := "后面的说明会继续生成并按顺序播放，你可以随时取消这一次回答。"
	if err := delta(last); err != nil {
		return "", err
	}
	return first + last, nil
}

func drainSegment(t *testing.T, d testDevice, start message, want int16) error {
	t.Helper()
	if err := d.send(message{Type: "credit", Turn: start.Turn, Frames: 32}); err != nil {
		return err
	}
	samples := 0
	seq := uint32(0)
	for {
		typ, b, err := d.read()
		if err != nil {
			return err
		}
		if typ == websocket.MessageBinary {
			f, e := decodeAudio(b)
			if e != nil {
				return e
			}
			if f.Turn != start.Turn || f.Seq != seq {
				return fmt.Errorf("wrong segment/sequence: turn=%d seq=%d", f.Turn, f.Seq)
			}
			seq++
			for _, v := range f.PCM {
				if v != want {
					return fmt.Errorf("out-of-order PCM: got=%d want=%d", v, want)
				}
			}
			samples += len(f.PCM)
		} else {
			m, e := decodeMessage(b)
			if e != nil {
				return e
			}
			if m.Type == "error" {
				return fmt.Errorf("device error: %s", m.Message)
			}
			if m.Type == "playback_end" {
				if m.Turn != start.Turn || samples != start.Samples {
					return fmt.Errorf("wrong drain count %d/%d turn=%d", samples, start.Samples, m.Turn)
				}
				break
			}
		}
	}
	if err := d.send(message{Type: "playing", Turn: start.Turn}); err != nil {
		return err
	}
	return d.send(message{Type: "played", Turn: start.Turn})
}

func TestSegmentedSessionPlaysBeforeLLMEOFAndIsolatesWireTurns(t *testing.T) {
	for _, scenario := range []string{"complete", "cancel_after_first", "finish_after_first"} {
		t.Run(scenario, func(t *testing.T) {
			cancelAfterFirst := scenario == "cancel_after_first"
			finishAfterFirst := scenario == "finish_after_first"
			release := make(chan struct{})
			exited := make(chan struct{})
			brain := &sessionStreamingBrain{release: release, exited: exited, endAfterFirst: finishAfterFirst}
			dep := fakeDependencies()
			dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { return brain, nil }
			var ttsCalls atomic.Int32
			dep.newSpeech = func(Config) speechAPI {
				return &fakeSpeech{synthesize: func(_ context.Context, text, _ string) ([]int16, error) {
					if strings.TrimSpace(text) == "" {
						return nil, fmt.Errorf("empty segment")
					}
					n := ttsCalls.Add(1)
					p := make([]int16, 641)
					for i := range p {
						p[i] = int16(n)
					}
					return p, nil
				}}
			}
			ready := make(chan struct{})
			finished := make(chan struct{})
			var commands chan Command
			commands, _, snapshots := sessionHarness(t, Config{Enabled: true, MiMoAPIKey: "test", LLMModel: "model"}, dep, func(d testDevice) error {
				<-ready
				commands <- Command{Kind: "wake"}
				if err := d.untilPhase("listening"); err != nil {
					return err
				}
				if err := d.mic(0, 9); err != nil {
					return err
				}
				for i := uint32(1); i <= 40; i++ {
					if err := d.mic(i, 0); err != nil {
						return err
					}
				}
				first, err := d.until("playback_start")
				if err != nil {
					return err
				}
				if first.Turn != 1 {
					return fmt.Errorf("initial wire turn=%d", first.Turn)
				}
				select {
				case <-exited:
					return fmt.Errorf("first audio waited until LLM EOF")
				default:
				}
				if err = drainSegment(t, d, first, 1); err != nil {
					return err
				}
				// The device has closed wire1, so the host reserves wire2 to hold synthesis
				// while the LLM is still blocked. It is still logical conversation1.
				if err = d.untilPhase("synthesizing"); err != nil {
					return err
				}
				if cancelAfterFirst {
					if err = d.send(message{Type: "cancel", Turn: first.Turn}); err != nil {
						return err
					}
					if err = d.untilPhase("idle"); err != nil {
						return err
					}
					select {
					case <-exited:
					case <-d.ctx.Done():
						return fmt.Errorf("cancel did not stop producer")
					}
				} else {
					close(release)
					if finishAfterFirst {
						if err = d.untilPhase("idle"); err != nil {
							return err
						}
						close(finished)
						return nil
					}
					second, err := d.until("playback_start")
					if err != nil {
						return err
					}
					if second.Turn <= first.Turn {
						return fmt.Errorf("wire identifier reused")
					}
					// Late credit/playing/played from clip1 must not affect clip2.
					for _, m := range []message{{Type: "credit", Turn: first.Turn, Frames: 32}, {Type: "playing", Turn: first.Turn}, {Type: "played", Turn: first.Turn}} {
						if err = d.send(m); err != nil {
							return err
						}
					}
					if err = drainSegment(t, d, second, 2); err != nil {
						return err
					}
					if err = d.untilPhase("idle"); err != nil {
						return err
					}
					// The next logical request is2, while the transport must advance beyond
					// both segments of conversation1. Old cancellation cannot cancel it.
					commands <- Command{Kind: "preview", Text: "下一轮试听。"}
					next, err := d.until("playback_start")
					if err != nil {
						return err
					}
					if next.Turn <= second.Turn {
						return fmt.Errorf("new request reused old segment identifier")
					}
					if err = d.send(message{Type: "cancel", Turn: first.Turn}); err != nil {
						return err
					}
					if err = drainSegment(t, d, next, 3); err != nil {
						return err
					}
					if err = d.untilPhase("idle"); err != nil {
						return err
					}
				}
				close(finished)
				return nil
			}, 8*time.Second)
			close(ready)
			select {
			case <-finished:
			case <-time.After(6 * time.Second):
				t.Fatal("segmented scenario stalled")
			}
			if cancelAfterFirst || finishAfterFirst {
				if ttsCalls.Load() != 1 {
					t.Fatal("cancel generated extra speech")
				}
			} else if ttsCalls.Load() != 3 {
				t.Fatalf("TTS calls=%d", ttsCalls.Load())
			}
			firstPlayed := false
			last := State{}
			for _, s := range snapshots() {
				if s.Phase == "error" {
					t.Fatalf("unexpected error: %s", s.Error)
				}
				if s.Turn == 1 && s.PlayedSegments == 1 && s.FirstAudioMillis > 0 {
					firstPlayed = true
				}
				last = s
			}
			if !firstPlayed {
				t.Fatal("no first-segment render timing recorded")
			}
			if !cancelAfterFirst && !finishAfterFirst && (last.Turn != 2 || last.PlayedSegments != 1) {
				t.Fatalf("transport segments leaked into UI round count: %+v", last)
			}
		})
	}
}
