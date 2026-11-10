//go:build (!darwin && !windows) || (darwin && !cgo)

package holdkey

import (
	"errors"
	"testing"
)

func TestUnsupported(t *testing.T) {
	stop, err := Start(func(bool) {})
	if stop != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Start = (%v, %v), want nil stop and ErrUnsupported", stop != nil, err)
	}
}
