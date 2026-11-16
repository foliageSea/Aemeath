package main

import (
	"runtime"

	"github.com/egoist/mygo"
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

var (
	nativeThemeMoonIcon = sidebarSVG("<path d='M20.9 13A9 9 0 0 1 11 3.1 9 9 0 1 0 20.9 13Z'/>")
	nativeThemeSunIcon  = sidebarSVG("<circle cx='12' cy='12' r='4'/><path d='M12 2v2M12 20v2M2 12h2M20 12h2M4.93 4.93l1.42 1.42M17.65 17.65l1.42 1.42M4.93 19.07l1.42-1.42M17.65 6.35l1.42-1.42'/>")
)

// setTheme keeps settings, title bar controls and native materials in sync.
// A nil UI override lets the host apply system appearance changes each frame.
func (a *nativeUI) setTheme(source mygo.ThemeSource) {
	switch source {
	case mygo.ThemeLight:
		a.theme = ui.LightTheme()
	case mygo.ThemeDark:
		a.theme = ui.DarkTheme()
	default:
		source = mygo.ThemeSystem
		a.theme = nil
	}
	// Context.SetTheme alone does not update native blur or window controls.
	a.themeWrite(source)
}

func (a *nativeUI) titleBar(c *ui.Context) {
	room := c.TitleBar()
	// Reserve the actual native controls on either side (traffic lights on
	// macOS, caption buttons on Windows/Linux). Symmetric insets keep the
	// title centered in the window even when only one side has controls.
	const buttonSize float32 = 32
	inset := max(room.Left, room.Right) + 16 + buttonSize + 12
	height := max(float32(nativeTitleBarHeight), room.Height)
	color := c.Theme().Text
	if !c.WindowActive() {
		color = c.Theme().TextMuted
	}
	ui.Row(c.Key("window-titlebar")).Label("窗口标题栏").FillWidth().
		Height(height).Shrink(0).
		Padding(0, inset).AlignItems(ui.Center).Justify(ui.Center).DragWindow().
		Background(nativeTitleBarBackground(c, runtime.GOOS)).
		BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border.Alpha(0.4)).Children(func() {
		ui.Text(c, "Aemeath").FontSize(13).Bold().TextColor(color).MinWidth(0).MaxLines(1).Ellipsis("…")
		label, icon := "切换到暗色模式", nativeThemeMoonIcon
		dark := c.Theme().Dark
		if dark {
			label, icon = "切换到亮色模式", nativeThemeSunIcon
		}
		toggle := ui.ButtonBase(c.Key("window-theme-toggle")).Label(label).
			Absolute().Right(room.Right+16).Top((height-buttonSize)/2).
			Size(buttonSize, buttonSize).Radius(8).Background(ui.Transparent).Center()
		if toggle.Hovered() {
			toggle.Background(c.Theme().SurfaceHover)
		}
		toggle.Children(func() {
			ui.Icon(c, icon).FontSize(17).TextColor(color)
		}).OnClick(func() {
			source := mygo.ThemeDark
			if dark {
				source = mygo.ThemeLight
			}
			a.setTheme(source)
		})
	})
}
