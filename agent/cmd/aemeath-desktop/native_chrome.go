package main

import (
	"runtime"

	"github.com/egoist/mygo/ui"
)

// The system material blurs the desktop behind the window, not the text or
// controls. A light, theme-aware tint connects the title bar and sidebar. On
// platforms without a material, keep both surfaces opaque and readable.
func nativeChromeBackground(c *ui.Context) ui.Color {
	if c.Vibrancy() && !c.Preferences().ReduceTransparency {
		return c.Theme().Surface.Alpha(0.18)
	}
	return c.Theme().Surface
}

func nativeTitleBarBackground(c *ui.Context, goos string) ui.Color {
	// MyGo extends the Windows DWM frame behind the content to show the
	// material. DWM still draws its caption glyphs there, underneath MyGo's
	// controls, at the system title bar height rather than our 48 DIPs. An
	// opaque title bar covers those glyphs; the sidebar can keep its material.
	if goos == "windows" {
		return c.Theme().Surface.Alpha(1)
	}
	return nativeChromeBackground(c)
}

func (a *nativeUI) titleBar(c *ui.Context) {
	room := c.TitleBar()
	// Reserve the actual native controls on either side (traffic lights on
	// macOS, caption buttons on Windows/Linux). Symmetric insets keep the
	// title centered in the window even when only one side has controls.
	inset := max(room.Left, room.Right) + 16
	color := c.Theme().Text
	if !c.WindowActive() {
		color = c.Theme().TextMuted
	}
	ui.Row(c.Key("window-titlebar")).Label("窗口标题栏").FillWidth().
		Height(max(float32(nativeTitleBarHeight), room.Height)).Shrink(0).
		Padding(0, inset).AlignItems(ui.Center).Justify(ui.Center).DragWindow().
		Background(nativeTitleBarBackground(c, runtime.GOOS)).
		BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border.Alpha(0.4)).Children(func() {
		ui.Text(c, "Aemeath").FontSize(13).Bold().TextColor(color).MinWidth(0).MaxLines(1).Ellipsis("…")
	})
}
