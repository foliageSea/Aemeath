//go:build (!darwin && !windows) || (darwin && !cgo)

package holdkey

import (
	"errors"
	"testing"
)

func TestUnsupported(t *testing.T) {
	for name, start := range map[string]func(func(bool)) (func(), error){
		"Start":          Start,
		"StartAlternate": StartAlternate,
		"StartShortcut":  func(callback func(bool)) (func(), error) { return StartShortcut("Ctrl+Space", callback) },
	} {
		t.Run(name, func(t *testing.T) {
			stop, err := start(func(bool) {})
			if stop != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("start = (%v, %v), want nil stop and ErrUnsupported", stop != nil, err)
			}
		})
	}
}
