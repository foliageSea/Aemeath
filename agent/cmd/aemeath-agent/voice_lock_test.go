package main

import (
	"context"
	"errors"
	"testing"
)

func TestCLICancelledLockDoesNotRequireDevice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&cliVoiceHost{}).LockComputer(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled local lock=%v", err)
	}
}
