//go:build !cgo || (!darwin && !windows && !linux)

package audiooutput

import (
	"strings"
	"testing"
)

func TestUnsupportedBuildReportsOutputError(t *testing.T) {
	if _, err := New(16000, 50); err == nil || !strings.Contains(err.Error(), "requires a cgo build") {
		t.Fatalf("unsupported output silently accepted: %v", err)
	}
}
