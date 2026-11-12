//go:build cgo && (darwin || windows || linux)

package audiooutput

import (
	"github.com/gen2brain/malgo"
	"testing"
)

func TestNativeBackendsNeverSilentlyDiscardAudio(t *testing.T) {
	for _, os := range []string{"darwin", "windows", "linux"} {
		backends := nativeBackends(os)
		if len(backends) == 0 {
			t.Fatalf("no real output backend for %s", os)
		}
		for _, backend := range backends {
			if backend == malgo.BackendNull {
				t.Fatalf("silent null fallback for %s", os)
			}
		}
	}
}
