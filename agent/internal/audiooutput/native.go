//go:build cgo && (darwin || windows || linux)

package audiooutput

import (
	"fmt"
	"runtime"

	"github.com/gen2brain/malgo"
)

// An explicit backend list prevents miniaudio's final null/silent fallback.
func nativeBackends(goos string) []malgo.Backend {
	switch goos {
	case "darwin":
		return []malgo.Backend{malgo.BackendCoreaudio}
	case "windows":
		return []malgo.Backend{malgo.BackendWasapi, malgo.BackendDsound, malgo.BackendWinmm}
	case "linux":
		return []malgo.Backend{malgo.BackendPulseaudio, malgo.BackendAlsa, malgo.BackendJack}
	default:
		return nil // startDevice is built only for the three platforms above.
	}
}

func startDevice(p *Player, sampleRate int) (func(), error) {
	ctx, err := malgo.InitContext(nativeBackends(runtime.GOOS), malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("initialize console audio output: %w", err)
	}
	release := func() { _ = ctx.Uninit(); ctx.Free() }
	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = 1
	cfg.SampleRate = uint32(sampleRate)
	cfg.PeriodSizeInMilliseconds = 20
	cfg.Periods = 3
	cfg.Alsa.NoMMap = 1
	dev, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{
		Data: func(out, _ []byte, _ uint32) { p.render(out) },
		Stop: p.stopped,
	})
	if err != nil {
		release()
		return nil, fmt.Errorf("open console audio output: %w", err)
	}
	if err := dev.Start(); err != nil {
		p.Close()
		dev.Uninit()
		release()
		return nil, fmt.Errorf("start console audio output: %w", err)
	}
	return func() { dev.Uninit(); release() }, nil
}
