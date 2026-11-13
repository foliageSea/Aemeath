//go:build !cgo || (!darwin && !windows)

package audioinput

import (
	"context"
	"strings"
	"testing"
)

func TestUnsupportedOpenIsActionable(t *testing.T) {
	in, err := Open(context.Background())
	if in != nil || err == nil || !strings.Contains(err.Error(), "cgo-enabled build on macOS or Windows") {
		t.Fatalf("unsupported Open = %v, %v", in, err)
	}
}
