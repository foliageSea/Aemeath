package voice

import (
	"aemeath/agent/internal/voiceapi"
	"aemeath/agent/internal/wake"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type hardwareCaptureVAD struct{ frames int }

func (v *hardwareCaptureVAD) Speech([]int16) (bool, error) { v.frames++; return v.frames == 1, nil }
func (v *hardwareCaptureVAD) Reset()                       { v.frames = 0 }
func (v *hardwareCaptureVAD) Close()                       {}

// Opt-in real-board protocol check. ASR, LLM and TTS are local fakes, so this
// validates DMA-drained segment transitions and sends no audio to cloud APIs.
// Quit other voice clients first; set AEMEATH_HW_ADDRESS=host to run explicitly.
func TestHardwareSegmentedPlayback(t *testing.T) {
	address := os.Getenv("AEMEATH_HW_ADDRESS")
	if address == "" {
		t.Skip("set AEMEATH_HW_ADDRESS for an explicit real-board playback check")
	}
	pcm, err := voiceapi.ReferencePCM(filepath.Join(wake.DefaultAssetDir(), "reference.wav"))
	if err != nil {
		t.Fatal(err)
	}
	dep := fakeDependencies()
	dep.newVAD = func(Config) (vad, error) { return &hardwareCaptureVAD{}, nil }
	release := make(chan struct{})
	exited := make(chan struct{})
	engine := &sessionStreamingBrain{release: release, exited: exited}
	dep.newBrain = func(context.Context, Config, Host) (brainAPI, error) { return engine, nil }
	dep.newSpeech = func(Config) speechAPI {
		return &fakeSpeech{synthesize: func(ctx context.Context, _ string, _ string) ([]int16, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return append([]int16(nil), pcm...), nil
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	commands := make(chan Command, 2)
	var startOnce, releaseOnce sync.Once
	var final State
	completed := false
	var deviceErr string
	err = runWithDependencies(ctx, address, Config{Enabled: true, Volume: 20, MiMoAPIKey: "local-fake-no-cloud", LLMModel: "local-fake-no-cloud"}, nil, commands, func(s State) {
		if s.Phase == "error" {
			deviceErr = s.Error
			cancel()
			return
		}
		if s.Connected && s.Phase == "idle" && s.Turn == 0 {
			startOnce.Do(func() { commands <- Command{Kind: "wake"} })
		}
		if s.PlayedSegments == 1 {
			releaseOnce.Do(func() { close(release) })
		}
		if s.Phase == "idle" && s.Turn == 1 && s.PlayedSegments == 2 {
			final = s
			completed = true
			cancel()
		}
	}, dep)
	if deviceErr != "" {
		t.Fatal(deviceErr)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !completed {
		t.Fatalf("board did not drain both segments: %+v", final)
	}
	if final.GeneratedSegments != 2 || final.FirstAudioMillis <= 0 {
		t.Fatalf("bad board playback metrics: %+v", final)
	}
	t.Logf("board drained %d segments in logical turn %d; first actual render=%dms (local fake cloud only); samples per segment=%d", final.PlayedSegments, final.Turn, final.FirstAudioMillis, len(pcm))
}
