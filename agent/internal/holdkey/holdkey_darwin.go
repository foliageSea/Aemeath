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

func start(callback func(bool), s shortcut) (func(), error) {
	key, modifiers, err := s.carbonCodes()
	if err != nil {
		return nil, err
	}
	events := newTransitions(callback)
	handle := cgo.NewHandle(events)
	var listener *C.HoldKeyListener
	status := C.holdkey_start(C.uintptr_t(handle), C.uint32_t(key), C.uint32_t(modifiers), &listener)
	if status != 0 {
		handle.Delete()
		events.shutdown()
		return nil, fmt.Errorf("holdkey: reserve global %s with Carbon: OSStatus %d (key may already be reserved)", s, int32(status))
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
