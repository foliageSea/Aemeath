//go:build !darwin && !windows

package sessionlock

import "context"

// Desktop login managers differ and may report success without locking any
// session. Explicit unsupported is preferable to a false successful lock.
func platformLock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnsupported
}
