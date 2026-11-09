//go:build darwin && !cgo

package sessionlock

import "context"

func platformLock(ctx context.Context) error {
	return lockCGSession(ctx, systemExecutable, runSystemCommand)
}
