package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const nativeTitleBarHeight = 48

func nativeWindowOptions(view func(*ui.Context), hidden bool) mygo.WindowOptions {
	return mygo.WindowOptions{
		// Keep the native window controls and shadow, drawing only the title
		// bar ourselves. Frameless would also remove the macOS traffic lights.
		TitleBarStyle:        mygo.TitleBarHiddenInset,
		TitleBarHeight:       nativeTitleBarHeight,
		TrafficLightPosition: &mygo.Point{X: 16, Y: (nativeTitleBarHeight - 14) / 2},
		Transparent:          true,
		Vibrancy:             mygo.VibrancySidebar,
		Title:                "Aemeath",
		Content:              ui.View(view),
		Width:                1100,
		Height:               800,
		MinWidth:             760,
		MinHeight:            600,
		DisableResize:        true,
		DisableMaximize:      true,
		DisableFullScreen:    true,
		Hidden:               hidden,
		StateKey:             "main-native-fixed",
	}
}
