package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeWindowDisablesUserResizeMaximizeAndFullscreen(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("hidden=%v", hidden), func(t *testing.T) {
			opts := nativeWindowOptions(func(*ui.Context) {}, hidden)
			if !opts.DisableResize || !opts.DisableMaximize || !opts.DisableFullScreen {
				t.Fatal("main window must not allow resizing, maximizing or fullscreen")
			}
			if opts.Width != 1100 || opts.Height != 800 || opts.Maximized || opts.FullScreen {
				t.Fatal("main window must open at its normal 1100x800 size")
			}
			if opts.Hidden != hidden || opts.Content == nil || opts.Title != "Aemeath" {
				t.Fatal("window restrictions changed initial visibility or content")
			}
			if opts.DisableMove || opts.DisableMinimize || opts.DisableClose {
				t.Fatal("moving, minimizing and closing must remain available")
			}
			if opts.StateKey != "main-native-fixed" {
				t.Fatal("fixed window must not restore the previous resizable window state")
			}
		})
	}
}
