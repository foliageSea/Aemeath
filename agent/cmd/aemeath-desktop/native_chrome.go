package main

import "github.com/egoist/mygo/ui"

// The system material blurs the desktop behind the window, not the text or
// controls. A light, theme-aware tint connects the title bar and sidebar. On
// platforms without a material, keep both surfaces opaque and readable.
func nativeChromeBackground(c *ui.Context) ui.Color {
	if c.Vibrancy() && !c.Preferences().ReduceTransparency {
		return c.Theme().Surface.Alpha(0.18)
	}
	return c.Theme().Surface
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
		Background(nativeChromeBackground(c)).
		BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border.Alpha(0.4)).Children(func() {
		ui.Text(c, "Aemeath").FontSize(13).Bold().TextColor(color).MinWidth(0).MaxLines(1).Ellipsis("…")
	})
}
