package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func nativeWindowOptions(view func(*ui.Context), hidden bool) mygo.WindowOptions {
	return mygo.WindowOptions{
		Title:             "Aemeath",
		Content:           ui.View(view),
		Width:             1100,
		Height:            800,
		MinWidth:          760,
		MinHeight:         600,
		DisableResize:     true,
		DisableMaximize:   true,
		DisableFullScreen: true,
		Hidden:            hidden,
		StateKey:          "main-native-fixed",
	}
}
