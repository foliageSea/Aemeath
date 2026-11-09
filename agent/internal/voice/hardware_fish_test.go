package voice

import (
	"aemeath/agent/internal/voiceapi"
	"aemeath/agent/internal/wake"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type hardwareFishSpeech struct {
	fakeSpeech
	pcm        []int16
	cancelCase bool
	finished   atomic.Bool
}

func (s *hardwareFishSpeech) Stream(ctx context.Context, text <-chan string, audio func([]int16) error) error {
	part := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-text:
			if !ok {
				if part != 2 {
					return errors.New("expected two local text groups")
				}
				s.finished.Store(true)
				return nil
			}
			part++
			var pcm []int16
			if part == 1 {
				pcm = s.pcm[:sampleRate]
			} else if part == 2 {
				pcm = s.pcm[sampleRate:]
			} else {
				return errors.New("unexpected third text group")
			}
			if err := audio(pcm); err != nil {
				return err
			}
			if part == 1 {
				if s.cancelCase {
					<-ctx.Done()
					return ctx.Err()
				}
				timer := time.NewTimer(1500 * time.Millisecond)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				}
				timer.Stop()
			}
		}
	}
}

// Local fake cloud only. A pause longer than the first audio chunk deliberately
// empties device DMA queues before stream end; this must NOT produce played.
func TestHardwareFishStreamPlayback(t *testing.T) {
	address := os.Getenv("AEMEATH_HW_ADDRESS")
	if address == "" {
		t.Skip("set AEMEATH_HW_ADDRESS for explicit real-board streaming validation")
	}
	pcm, err := voiceapi.ReferencePCM(filepath.Join(wake.DefaultAssetDir(), "reference.wav"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cancelCase := range []bool{false, true} {
		name := "drain"
		if cancelCase {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			release := make(chan struct{})
			engine := &sessionStreamingBrain{release: release, exited: make(chan struct{})}
			speech := &hardwareFishSpeech{pcm: pcm, cancelCase: cancelCase}
			dep := fakeDependencies()
			dep.newVAD = func(Config) (vad, error) { return &hardwareCaptureVAD{}, nil }
			dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { return engine, nil }
			dep.newSpeech = func(Config) speechAPI { return speech }
			cmds := make(chan Command, 2)
			var startOnce, playingOnce sync.Once
			var final State
			var failure string
			completed := false
			err := runWithDependencies(ctx, address, Config{Enabled: true, Volume: 20, TTSProvider: "fish", FishAPIKey: "local-fake-no-cloud", FishReferenceID: "local-fake-voice", MiMoAPIKey: "local-fake-no-cloud", LLMModel: "local-fake"}, nil, cmds, func(s State) {
				if s.Phase == "error" {
					failure = s.Error
					cancel()
					return
				}
				if s.Connected && s.Phase == "idle" && s.Turn == 0 {
					startOnce.Do(func() { cmds <- Command{Kind: "wake"} })
				}
				if s.Phase == "speaking" {
					playingOnce.Do(func() {
						close(release)
						if cancelCase {
							cmds <- Command{Kind: "cancel"}
						}
					})
				}
				if s.Turn == 1 && s.Phase == "idle" && s.FirstAudioMillis > 0 {
					if !cancelCase && (!speech.finished.Load() || s.PlayedSegments != 1) {
						failure = "device completed stream before successful backend finish"
					}
					if cancelCase && s.PlayedSegments != 0 {
						failure = "cancelled stream incorrectly marked played"
					}
					final = s
					completed = true
					cancel()
				}
			}, dep)
			if failure != "" {
				t.Fatal(failure)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if !completed {
				t.Fatalf("hardware stream did not complete mode=%s", name)
			}
			t.Logf("local fake Fish mode=%s logicalturn=%d streams=%d drained=%d firstactualrender=%dms source_samples=%d", name, final.Turn, final.GeneratedSegments, final.PlayedSegments, final.FirstAudioMillis, len(pcm))
		})
	}
}
