//go:build cgo && (darwin || windows)

package audioinput

import (
	"testing"

	"github.com/gen2brain/malgo"
)

func TestNativeConfigUsesDefaultMono16kS16(t *testing.T) {
	cfg := nativeConfig()
	if cfg.DeviceType != malgo.Capture || cfg.Capture.DeviceID != nil || cfg.Capture.Format != malgo.FormatS16 || cfg.Capture.Channels != 1 || cfg.SampleRate != 16000 || cfg.PeriodSizeInFrames != 320 {
		t.Fatalf("incorrect capture configuration: %+v", cfg)
	}
	for _, goos := range []string{"darwin", "windows"} {
		backends := nativeBackends(goos)
		if len(backends) == 0 {
			t.Fatalf("no real capture backend for %s", goos)
		}
		for _, backend := range backends {
			if backend == malgo.BackendNull {
				t.Fatalf("silent null fallback for %s", goos)
			}
		}
	}
}
