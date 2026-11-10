//go:build darwin && cgo

package holdkey

/*
#cgo LDFLAGS: -framework Carbon
#include "holdkey_darwin.h"
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"sync"
)

func start(callback func(bool), modified bool) (func(), error) {
	events := newTransitions(callback)
	handle := cgo.NewHandle(events)
	var listener *C.HoldKeyListener
	var modifiers C.int
	if modified {
		modifiers = 1
	}
	status := C.holdkey_start(C.uintptr_t(handle), modifiers, &listener)
	if status != 0 {
		handle.Delete()
		events.shutdown()
		return nil, fmt.Errorf("holdkey: reserve global F8 with Carbon: OSStatus %d (key may already be reserved)", int32(status))
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			C.holdkey_stop(listener)
			handle.Delete()
			events.shutdown()
		})
	}, nil
}

//export holdkey_changed
func holdkey_changed(token C.uintptr_t, down C.int) {
	cgo.Handle(token).Value().(*transitions).change(down != 0)
}
