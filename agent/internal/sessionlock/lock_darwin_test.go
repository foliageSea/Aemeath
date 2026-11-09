//go:build darwin && cgo

package sessionlock

import (
	"context"
	"errors"
	"testing"
)

// Inject fake native requests. Never invoke the real OS lock from a test.
func TestNativeRequestStatusAndCleanup(t *testing.T) {
	for _, status := range []int32{0, -1} {
		called, closed := 0, 0
		err := lockNative(context.Background(), func() (func() int32, func(), bool) {
			return func() int32 { called++; return status }, func() { closed++ }, true
		})
		if (err != nil) != (status != 0) || called != 1 || closed != 1 {
			t.Fatalf("status=%d err=%v calls=%d closes=%d", status, err, called, closed)
		}
	}
}
func TestNativeUnavailableAndLateCancellation(t *testing.T) {
	if err := lockNative(context.Background(), func() (func() int32, func(), bool) { return nil, nil, false }); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	called, closed := false, false
	err := lockNative(ctx, func() (func() int32, func(), bool) {
		cancel()
		return func() int32 { called = true; return 0 }, func() { closed = true }, true
	})
	if !errors.Is(err, context.Canceled) || called || !closed {
		t.Fatalf("err=%v called=%v closed=%v", err, called, closed)
	}
}
