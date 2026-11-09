package desktop

import (
	"context"
	"errors"
	"testing"
)

func TestVoiceHostCancelledLockDoesNotRequireDevice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (voiceHost{}).LockComputer(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled local lock=%v", err)
	}
}
