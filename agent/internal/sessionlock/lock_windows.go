//go:build windows

package sessionlock

import (
	"context"
	"golang.org/x/sys/windows"
)

var lockWorkStation = windows.NewLazySystemDLL("user32.dll").NewProc("LockWorkStation")

func platformLock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := lockWorkStation.Find(); err != nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result, _, _ := lockWorkStation.Call()
	if result == 0 {
		return ErrRejected
	}
	return nil
}
