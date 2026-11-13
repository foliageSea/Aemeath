//go:build cgo && darwin

package audioinput

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These exercise the permission wait logic without invoking AVFoundation's
// requestAccess, opening a microphone, or changing the machine's TCC state.
func TestPermissionWaitCancellationAndLateResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var result atomic.Int32
	queried := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- waitPermission(ctx, func() int {
			select {
			case <-queried:
			default:
				close(queried)
			}
			return int(result.Load())
		})
	}()
	select {
	case <-queried:
	case <-time.After(2 * time.Second):
		t.Fatal("permission wait did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("permission cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("permission request did not cancel")
	}
	result.Store(1) // A completion after cancellation is allowed.
}

func TestPermissionResultsAreActionable(t *testing.T) {
	for _, test := range []struct {
		status   int
		contains string
	}{{1, ""}, {-1, "Privacy & Security > Microphone"}, {-2, "administrator"}} {
		err := waitPermission(context.Background(), func() int { return test.status })
		if test.contains == "" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), test.contains) {
			t.Fatalf("permission %d error = %v", test.status, err)
		}
	}
}
