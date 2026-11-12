//go:build !darwin && !windows

package main

import (
	"fmt"
	"runtime"

	"github.com/egoist/mygo"
)

// Do not silently claim keyboard-focus isolation on unvalidated backends.
// The caller may keep the pet hidden and surface this error non-disruptively.
func petPlatformPanel() bool { return false }

func connectPetPointer(*mygo.Window, *nativePet) bool { return false }

func petWindowMover(w *mygo.Window) func(mygo.Rectangle) {
	return func(r mygo.Rectangle) { w.SetPosition(r.X, r.Y) }
}

func configurePetWindow(w *mygo.Window) error {
	return fmt.Errorf("pet window: non-focusing windows are not supported on %s", runtime.GOOS)
}
