package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNativeWindowUsesCustomTitleBarAndNativeControls(t *testing.T) {
	opts := nativeWindowOptions(func(*ui.Context) {}, false)
	if opts.TitleBarStyle != mygo.TitleBarHiddenInset || opts.Frameless || opts.HideWindowButtons {
		t.Fatal("custom title bar must retain native window controls and shadow")
	}
	if opts.TitleBarHeight != nativeTitleBarHeight || opts.TrafficLightPosition == nil {
		t.Fatal("system buttons must be positioned inside the custom title bar")
	}
	p := opts.TrafficLightPosition
	if p.X < 0 || p.Y < 0 || p.Y+14 > nativeTitleBarHeight {
		t.Fatalf("traffic lights fall outside title bar: %+v", p)
	}
	if !opts.Transparent || opts.Vibrancy != mygo.VibrancySidebar {
		t.Fatal("title bar and sidebar must reveal the native blurred material")
	}
}

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
