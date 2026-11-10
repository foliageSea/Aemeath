package main

import (
	"path/filepath"

	"aemeath/agent/internal/wake"
	"github.com/egoist/mygo/ui"
)

var (
	wakeDirectoryIcon   = sidebarSVG("<path d='M3 7V5a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z'/>")
	wakeDirectoryClosed = sidebarSVG("<path d='m9 6 6 6-6 6'/>")
	wakeDirectoryOpen   = sidebarSVG("<path d='m6 9 6 6 6-6'/>")
)

func (a *nativeUI) wakeDirectoryView(c *ui.Context, busy bool) {
	t := c.Theme()
	target, defaultDir := a.wakeModelPath(), wake.DefaultModelDir()
	usingDefault := target == filepath.Clean(defaultDir)
	label, icon := "模型目录与高级选项", wakeDirectoryClosed
	if a.wakeAdvanced {
		label, icon = "收起模型目录", wakeDirectoryOpen
	}
	ui.Column(c.Key("wake-directory-section")).FillWidth().MinWidth(0).Gap(8).Children(func() {
		button := ui.Button(c.Key("wake-advanced"), "").Label(label).Expanded(a.wakeAdvanced).FillWidth().MinWidth(0).Height(34).Padding(6, 8).Gap(7).BorderWidth(0).Radius(7).AlignItems(ui.Center).Justify(ui.Start).Background(ui.Color{})
		if button.Hovered() {
			button.Background(t.SurfaceHover)
		}
		button.Children(func() {
			ui.Icon(c, wakeDirectoryIcon).Size(16, 16).Shrink(0).TextColor(t.TextMuted)
			ui.Text(c, "模型目录").FontSize(12).Grow(1).MinWidth(0)
			mode := "自定义"
			if usingDefault {
				mode = "默认"
			}
			ui.Text(c, mode).FontSize(10).TextColor(t.TextMuted).Padding(2, 6).Radius(4).Background(t.Border.Alpha(0.25))
			ui.Icon(c, icon).Size(14, 14).Shrink(0).TextColor(t.TextMuted)
		}).OnClick(func() { a.wakeAdvanced = !a.wakeAdvanced })
		if a.wakeAdvanced {
			ui.Column(c.Key("wake-directory-editor")).Label("模型目录设置").FillWidth().MinWidth(0).Padding(12).Gap(8).Radius(8).Background(t.Background.Alpha(0.55)).Border(1, t.Border.Alpha(0.45)).Children(func() {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "唤醒模型目录").FontSize(11).TextColor(t.TextMuted).Grow(1).MinWidth(0)
					ui.Button(c.Key("wake-default-dir"), "恢复默认").Label("使用默认模型目录").Tooltip(defaultDir).FontSize(11).Height(24).Padding(3, 5).BorderWidth(0).Background(ui.Color{}).TextColor(t.Accent).Disabled(busy || usingDefault).OnClick(func() {
						a.draft.Voice.WakeModelDir = defaultDir
						a.edited()
					})
				})
				if ui.TextInput(c.Key("voice-wake-dir"), &a.draft.Voice.WakeModelDir).Label("唤醒模型目录").Placeholder(defaultDir).Tooltip(target).FontSize(12).FillWidth().MinWidth(0).Height(34).Disabled(busy).Changed() {
					a.edited()
				}
				ui.Text(c, "下载与检查均使用此目录，更改后请保存设置。").FontSize(11).TextColor(t.TextMuted).Wrap()
			})
		}
	})
}
