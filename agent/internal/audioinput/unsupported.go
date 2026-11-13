//go:build !cgo || (!darwin && !windows)

package audioinput

import (
	"context"
	"errors"
)

func unsupportedError() error {
	return errors.New("native microphone capture requires a cgo-enabled build on macOS or Windows")
}

func requestPermission(context.Context) error               { return unsupportedError() }
func newDevice(func([]byte), func()) (captureDevice, error) { return nil, unsupportedError() }
