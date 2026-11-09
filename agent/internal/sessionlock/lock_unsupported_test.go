//go:build !darwin && !windows

package sessionlock

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupported(t *testing.T) {
	if err := Lock(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
