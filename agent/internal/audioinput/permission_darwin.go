//go:build cgo && darwin

package audioinput

/*
#cgo CFLAGS: -x objective-c -fblocks
#cgo LDFLAGS: -framework AVFoundation -framework Foundation
#include "permission_darwin.h"
*/
import "C"

import (
	"context"
	"errors"
	"time"
)

func permissionError(status int) error {
	if status == -2 {
		return errors.New("microphone access is restricted by macOS policy; ask your administrator to allow microphone access")
	}
	return errors.New("microphone access was denied; allow this application (or its terminal host) in System Settings > Privacy & Security > Microphone, then restart it")
}

func requestPermission(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	status := int(C.aemeath_mic_permission_status())
	if status == 1 {
		return nil
	}
	if status < 0 {
		return permissionError(status)
	}
	if C.aemeath_mic_has_usage_description() == 0 {
		return errors.New("microphone permission cannot be requested: the macOS application or executable Info.plist must include NSMicrophoneUsageDescription; install a build with microphone permission metadata")
	}
	request := C.aemeath_mic_request_begin()
	if request == nil {
		return errors.New("allocate macOS microphone permission request")
	}
	// Cancellation releases only Go's reference. A late completion accesses only C
	// memory and releases its own reference; no Go handles or pointers escape.
	defer C.aemeath_mic_request_release(request)
	return waitPermission(ctx, func() int { return int(C.aemeath_mic_request_result(request)) })
}

func waitPermission(ctx context.Context, result func() int) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		status := result()
		if status == 1 {
			return nil
		}
		if status < 0 {
			return permissionError(status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
