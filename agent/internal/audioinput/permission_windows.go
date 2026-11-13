//go:build cgo && windows

package audioinput

import "context"

// Windows desktop microphone access is controlled in Settings; the native open
// error provides the controls to check when access is denied.
func requestPermission(ctx context.Context) error { return ctx.Err() }
