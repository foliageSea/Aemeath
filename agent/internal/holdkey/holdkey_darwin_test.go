//go:build darwin && cgo

package holdkey

import (
	"fmt"
	"strings"
	"testing"
)

func TestMacOSRejectsF21ThroughF24(t *testing.T) {
	for n := 21; n <= 24; n++ {
		key := fmt.Sprintf("Ctrl+F%d", n)
		stop, err := StartShortcut(key, func(bool) { t.Error("unsupported key invoked callback") })
		if stop != nil || err == nil || !strings.Contains(err.Error(), "unsupported on macOS") {
			t.Fatalf("StartShortcut(%q)=(%v,%v), want explicit macOS rejection", key, stop != nil, err)
		}
	}
}
