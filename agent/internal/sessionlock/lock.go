// Package sessionlock requests a real lock of the current OS user session.
// A successful request does not attest that the lock screen has finished drawing.
package sessionlock

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnsupported = errors.New("session locking is unsupported on this platform")
	ErrUnavailable = errors.New("the operating system session lock API is unavailable")
	ErrRejected    = errors.New("the operating system rejected the session lock request")
)

const requestTimeout = 3 * time.Second

// Lock requests a lock of the current logged-in session. It never sleeps the
// display, simulates keystrokes, or accepts a user-controlled shell command.
// Cancellation is checked immediately before issuing the OS request. Once
// accepted, an OS lock request cannot be undone by cancelling its context.
func Lock(ctx context.Context) error { return lockWith(ctx, platformLock) }

func lockWith(ctx context.Context, request func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return request(bounded)
}

func checkedStatus(status int32) error {
	if status != 0 {
		return ErrRejected
	}
	return nil
}
