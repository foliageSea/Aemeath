//go:build windows

package main

import (
	"fmt"
	"syscall"

	"github.com/egoist/mygo"
)

var (
	petUser32        = syscall.NewLazyDLL("user32.dll")
	petGetWindowLong = petUser32.NewProc("GetWindowLongW")
	petSetWindowLong = petUser32.NewProc("SetWindowLongW")
	petSetWindowPos  = petUser32.NewProc("SetWindowPos")
	petSetLastError  = syscall.NewLazyDLL("kernel32.dll").NewProc("SetLastError")
)

// configurePetWindow preserves all existing extended styles and the window
// level. Create Hidden, configure, then use ShowInactive, never Show/Focus.
// WS_EX_NOACTIVATE prevents normal mouse activation; explicit SetFocus or
// SetForegroundWindow can override it. MyGo v0.8.3 native surfaces call SetFocus
// on pointer-down, so real cross-app click/typing validation remains essential
// on Windows; this style alone is not a promise about that explicit SDK call.
func petPlatformPanel() bool { return false }

func connectPetPointer(*mygo.Window, *nativePet) bool { return false }

func petWindowMover(w *mygo.Window) func(mygo.Rectangle) {
	return func(r mygo.Rectangle) { w.SetPosition(r.X, r.Y) }
}

func configurePetWindow(w *mygo.Window) error {
	if w == nil {
		return fmt.Errorf("pet window: nil window")
	}
	err := fmt.Errorf("pet window: UI event loop is not running")
	mygo.RunOnMain(func() {
		handle := w.NativeHandle()
		if handle == 0 {
			err = fmt.Errorf("pet window: native window is unavailable")
			return
		}
		// GWL_EXSTYLE is a 32-bit LONG even on 64-bit Windows. Use the W
		// LONG APIs on both architectures, retaining every existing bit.
		index := ^uintptr(19) // GWL_EXSTYLE = -20
		const noActivate = uint32(0x08000000)
		petSetLastError.Call(0)
		style, _, readErr := petGetWindowLong.Call(handle, index)
		if style == 0 && readErr != syscall.Errno(0) {
			err = fmt.Errorf("pet window: read extended style: %w", readErr)
			return
		}
		petSetLastError.Call(0)
		old, _, writeErr := petSetWindowLong.Call(handle, index, uintptr(uint32(style)|noActivate))
		if old == 0 && writeErr != syscall.Errno(0) {
			err = fmt.Errorf("pet window: set WS_EX_NOACTIVATE: %w", writeErr)
			return
		}
		// Apply the changed frame without moving, resizing, reordering or
		// activating it (NOSIZE|NOMOVE|NOZORDER|NOACTIVATE|FRAMECHANGED).
		ok, _, applyErr := petSetWindowPos.Call(handle, 0, 0, 0, 0, 0, 0x0037)
		if ok == 0 {
			err = fmt.Errorf("pet window: apply non-activating style: %w", applyErr)
			return
		}
		err = nil
	})
	return err
}
