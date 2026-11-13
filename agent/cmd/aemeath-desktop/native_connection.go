package main

import (
	"aemeath/agent/internal/desktop"
	"github.com/egoist/mygo/ui"
)

func (a *nativeUI) sidebarConnection(c *ui.Context) {
	t := c.Theme()
	if a.snapshot.Settings.RunMode == desktop.RunModeStandalone {
		label, color := "独立模式 · 已停止", t.TextMuted
		if a.snapshot.Running {
			label, color = "独立模式 · 启动中", t.Warning
			if !a.snapshot.Settings.Voice.Enabled {
				label, color = "独立模式 · 语音未启用", t.TextMuted
			}
			if a.snapshot.Voice.Connected {
				label, color = "独立模式 · 就绪", t.Success
			}
			if a.snapshot.Voice.Error != "" {
				label, color = "独立模式 · 语音异常", t.Warning
			}
		}
		ui.Row(c.Key("sidebar-standalone")).Label("独立模式状态").FillWidth().Shrink(0).Height(31).Padding(3, 7).Gap(9).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(6, 6).Shrink(0).Radius(3).Background(color)
			ui.Text(c, label).FontSize(12).TextColor(color).Grow(1).MinWidth(0).SingleLine().Tooltip(a.snapshot.Voice.Error)
		})
		return
	}
	label, color := nativeStatus(a.snapshot.Status), t.TextMuted
	if label == "" {
		label = "设备未连接"
	}
	if a.snapshot.Connected {
		label, color = "设备已连接", t.Success
	} else if a.snapshot.Running {
		color = t.Warning
	}
	hasError := a.snapshot.Error != ""
	if hasError {
		label, color = "连接异常", t.Warning
	}
	content := func() {
		ui.Box(c).Size(6, 6).Shrink(0).Radius(3).Background(color)
		ui.Text(c, label).FontSize(12).Grow(1).MinWidth(0).SingleLine()
		if hasError {
			arrow := sidebarChevronRight
			if a.details {
				arrow = sidebarChevronDown
			}
			ui.Icon(c, arrow).Size(14, 14).Shrink(0).TextColor(color)
		}
	}
	if hasError {
		ui.Button(c.Key("sidebar-connection"), "").Label("连接异常 · 查看详情").Expanded(a.details).Tooltip("查看设备连接详情").FillWidth().Shrink(0).Height(31).Padding(3, 7).Gap(9).AlignItems(ui.Center).Justify(ui.Start).BorderWidth(0).Radius(7).Background(color.Alpha(0.1)).TextColor(color).Children(content).OnClick(func() { a.details = !a.details })
	} else {
		ui.Row(c.Key("sidebar-connection")).Label("设备连接状态").FillWidth().Shrink(0).Height(31).Padding(3, 7).Gap(9).AlignItems(ui.Center).TextColor(color).Children(content)
	}
	if hasError && a.details {
		ui.Scroll(c.Key("sidebar-connection-details")).Label("设备连接错误详情").FillWidth().MinWidth(0).Shrink(0).Height(104).Children(func() {
			ui.Column(c).FillWidth().MinWidth(0).Padding(8, 7).Children(func() {
				ui.Text(c, "设备连接："+a.snapshot.Error).FontSize(11).TextColor(t.TextMuted).Wrap().Selectable().Tooltip(a.snapshot.Error)
			})
		})
	}
}
