//go:build cgo && (darwin || windows)

package audioinput

import (
	"fmt"
	"runtime"

	"github.com/gen2brain/malgo"
)

type nativeDevice struct {
	device  *malgo.Device
	context *malgo.AllocatedContext
}

func (d *nativeDevice) Start() error {
	if err := d.device.Start(); err != nil {
		return nativeError("start", err)
	}
	return nil
}

func (d *nativeDevice) Close() {
	d.device.Uninit()
	_ = d.context.Uninit()
	d.context.Free()
}

func nativeConfig() malgo.DeviceConfig {
	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	// A nil DeviceID selects the system default, rather than the first enumerated mic.
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.SampleRate = sampleRate
	cfg.PeriodSizeInFrames = frameSamples
	cfg.Periods = 3
	return cfg
}

func nativeBackends(goos string) []malgo.Backend {
	switch goos {
	case "darwin":
		return []malgo.Backend{malgo.BackendCoreaudio}
	case "windows":
		return []malgo.Backend{malgo.BackendWasapi, malgo.BackendDsound, malgo.BackendWinmm}
	default:
		return nil
	}
}

func nativeError(action string, err error) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("%s default microphone: %w; check Settings > Privacy & security > Microphone, enable microphone access and desktop app access, and select an available default input in Sound settings", action, err)
	}
	return fmt.Errorf("%s default microphone: %w; check System Settings > Privacy & Security > Microphone and Sound > Input", action, err)
}

func newDevice(data func([]byte), stopped func()) (captureDevice, error) {
	// Explicit real backends prevent miniaudio's null-device fallback.
	ctx, err := malgo.InitContext(nativeBackends(runtime.GOOS), malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, nativeError("initialize", err)
	}
	dev, err := malgo.InitDevice(ctx.Context, nativeConfig(), malgo.DeviceCallbacks{
		Data: func(_ []byte, input []byte, frames uint32) {
			if uint64(len(input)) != uint64(frames)*2 {
				// A missing native buffer must end capture rather than lose audio.
				stopped()
				return
			}
			data(input)
		},
		Stop: stopped,
	})
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return nil, nativeError("open", err)
	}
	return &nativeDevice{device: dev, context: ctx}, nil
}
