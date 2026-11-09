package sessionlock

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLockWithChecksCancellationBeforeRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := lockWith(ctx, func(context.Context) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}
func TestLockWithBoundsRequest(t *testing.T) {
	before := time.Now()
	called := false
	err := lockWith(context.Background(), func(ctx context.Context) error {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(before.Add(requestTimeout+time.Second)) {
			t.Fatal("missing bounded deadline")
		}
		return ErrRejected
	})
	if !called || !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := short.Deadline()
	if err := lockWith(short, func(ctx context.Context) error {
		got, _ := ctx.Deadline()
		if !got.Equal(want) {
			t.Fatal("extended caller deadline")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestStaticCGSessionRequest(t *testing.T) {
	calls := 0
	err := lockCGSession(context.Background(), func(name string) bool { return name == cgSessionPath }, func(ctx context.Context, name string, args ...string) error {
		calls++
		if name != cgSessionPath || len(args) != 1 || args[0] != "-suspend" {
			t.Fatal("unexpected command")
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
func TestCGSessionErrorsAndCancellation(t *testing.T) {
	sentinel := errors.New("secret external output")
	for _, tc := range []struct {
		name         string
		available    bool
		runErr, want error
	}{{"unavailable", false, nil, ErrUnavailable}, {"rejected", true, sentinel, ErrRejected}} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := lockCGSession(context.Background(), func(string) bool { return tc.available }, func(context.Context, string, ...string) error { called = true; return tc.runErr })
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
			if called != tc.available {
				t.Fatal("unexpected request")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	called := false
	err := lockCGSession(ctx, func(string) bool { cancel(); return true }, func(context.Context, string, ...string) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}
func TestCheckedStatus(t *testing.T) {
	if checkedStatus(0) != nil {
		t.Fatal("zero status rejected")
	}
	if !errors.Is(checkedStatus(-1), ErrRejected) {
		t.Fatal("nonzero status accepted")
	}
}
