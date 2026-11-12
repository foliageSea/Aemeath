package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func sidebarSVG(paths string) *ui.SVG {
	return ui.MustParseSVG([]byte("<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='1.7' stroke-linecap='round' stroke-linejoin='round'>" + paths + "</svg>"))
}

var (
	sidebarOverview      = sidebarSVG("<rect x='3' y='3' width='7' height='7' rx='1'/><rect x='14' y='3' width='7' height='7' rx='1'/><rect x='3' y='14' width='7' height='7' rx='1'/><rect x='14' y='14' width='7' height='7' rx='1'/>")
	sidebarSubscriptions = sidebarSVG("<path d='M9 5h11M9 12h11M9 19h11'/><circle cx='4' cy='5' r='0.7'/><circle cx='4' cy='12' r='0.7'/><circle cx='4' cy='19' r='0.7'/>")
	sidebarVoice         = sidebarSVG("<rect x='9' y='2' width='6' height='13' rx='3'/><path d='M5 10v2a7 7 0 0 0 14 0v-2M12 19v3M8 22h8'/>")
	sidebarSettings      = sidebarSVG("<circle cx='12' cy='12' r='4'/><path d='M12 2v3M12 19v3M2 12h3M19 12h3M5 5l2 2M17 17l2 2M5 19l2-2M17 7l2-2'/>")
	sidebarCPU           = sidebarSVG("<rect x='6' y='6' width='12' height='12' rx='2'/><rect x='9' y='9' width='6' height='6' rx='1'/><path d='M9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4'/>")
	sidebarMemory        = sidebarSVG("<rect x='3' y='6' width='18' height='12' rx='2'/><path d='M7 10v4M12 10v4M17 10v4M7 18v3M12 18v3M17 18v3'/>")
	sidebarGPU           = sidebarSVG("<path d='M12 2 21 7v10l-9 5-9-5V7l9-5ZM3 7l9 5 9-5M12 12v10M8 4.2l9 5'/>")
	sidebarDisk          = sidebarSVG("<rect x='3' y='5' width='18' height='14' rx='4'/><path d='M4 13h16M15 16h2'/><circle cx='7' cy='16' r='0.7'/>")
	sidebarNetwork       = sidebarSVG("<circle cx='12' cy='12' r='9'/><ellipse cx='12' cy='12' rx='4' ry='9'/><path d='M3 12h18M5 6.5c4 2 10 2 14 0M5 17.5c4-2 10-2 14 0'/>")
	sidebarPower         = sidebarSVG("<path d='M12 2v10M6 5a9 9 0 1 0 12 0'/>")
	sidebarChevronRight  = sidebarSVG("<path d='M9 6l6 6-6 6'/>")
	sidebarChevronDown   = sidebarSVG("<path d='M6 9l6 6 6-6'/>")
)

func sidebarIcon(c *ui.Context, icon *ui.SVG, background ui.Color) {
	ui.Box(c).Size(23, 23).Shrink(0).Radius(6).Background(background).Center().Children(func() {
		ui.Icon(c, icon).FontSize(16).TextColor(ui.RGB(255, 255, 255))
	})
}

func (a *nativeUI) sidebarItem(c *ui.Context, key, page string, icon *ui.SVG, count string) {
	t := c.Theme()
	selected := a.page == page
	button := ui.Button(c.Key(key), "").Label(page).AccessSelected(selected).BorderWidth(0).Background(ui.Color{}).FillWidth().Height(31).Padding(3, 7).Gap(9).Radius(7).AlignItems(ui.Center).Justify(ui.Start)
	badge := ui.RGB(120, 121, 127)
	if selected {
		button.Background(t.Accent).TextColor(t.AccentText)
		badge = t.AccentText.Alpha(0.18)
	} else if button.Hovered() {
		button.Background(t.SurfaceHover)
	}
	button.Children(func() {
		sidebarIcon(c, icon, badge)
		ui.Text(c, page).FontSize(13).Grow(1).MinWidth(0).SingleLine()
		if count != "" {
			color := t.TextMuted
			if selected {
				color = t.AccentText.Alpha(0.8)
			}
			ui.Text(c, count).FontSize(12).TextColor(color).SingleLine()
		}
	}).OnClick(func() { a.page = page })
}

func (a *nativeUI) sidebarValues() [5]string {
	values := [5]string{"—", "—", "—", "—", "—"}
	m := a.snapshot.Metrics
	if m == nil {
		return values
	}
	if m.CPU != nil {
		values[0] = nativeNumber(m.CPU.Total, "%")
	}
	if m.Mem != nil && m.Mem.Total > 0 {
		values[1] = fmt.Sprintf("%.0f%%", float64(m.Mem.Used)/float64(m.Mem.Total)*100)
	}
	if m.GPU != nil {
		values[2] = nativeNumber(m.GPU.Load, "%")
	}
	if m.Disk != nil && m.Disk.Read != nil && m.Disk.Write != nil {
		rate := *m.Disk.Read + *m.Disk.Write
		values[3] = nativeRate(&rate)
	}
	if m.Net != nil {
		values[4] = nativeRate(m.Net.Down)
	}
	return values
}

func (a *nativeUI) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("sidebar")).Width(206).Shrink(0).Padding(12, 10).Gap(3).Background(nativeChromeBackground(c)).BorderWidth(0, 1, 0, 0).BorderColor(t.Border.Alpha(0.5)).Children(func() {
		ui.Scroll(c.Key("sidebar-navigation")).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Gap(3).Children(func() {
				a.sidebarItem(c, "nav-overview", "概览", sidebarOverview, "")
				count := ""
				if n := len(a.subscriptions()); n > 0 {
					count = fmt.Sprint(n)
				}
				a.sidebarItem(c, "nav-subscriptions", "订阅", sidebarSubscriptions, count)
				a.sidebarItem(c, "nav-voice", "语音助手", sidebarVoice, "")
				ui.Text(c, "系统").FontSize(11).TextColor(t.TextMuted).Padding(15, 7, 5)
				a.sidebarItem(c, "nav-settings", "设置", sidebarSettings, "")
				ui.Text(c, "硬件").FontSize(11).TextColor(t.TextMuted).Padding(15, 7, 5)
				values := a.sidebarValues()
				icons := []*ui.SVG{sidebarCPU, sidebarMemory, sidebarGPU, sidebarDisk, sidebarNetwork}
				for i, label := range []string{"CPU", "内存", "GPU", "磁盘", "网络"} {
					ui.Row(c.Key("sidebar-metric-"+label)).FillWidth().Height(31).Padding(3, 7).Gap(9).AlignItems(ui.Center).Children(func() {
						sidebarIcon(c, icons[i], metricColor(i))
						ui.Text(c, label).FontSize(13).Grow(1).MinWidth(0).SingleLine()
						ui.Text(c, values[i]).FontSize(12).TextColor(t.TextMuted).SingleLine()
					})
				}
			})
		})
		host, detail := "本机", ""
		if m := a.snapshot.Metrics; m != nil {
			if m.Host != "" {
				host = m.Host
			}
			detail = m.OS
			if m.CPU != nil && m.CPU.Name != "" {
				detail = m.CPU.Name
			}
			if m.Uptime != nil {
				detail += fmt.Sprintf(" · %dh", *m.Uptime/3600)
			}
		}
		ui.Column(c).FillWidth().Shrink(0).Padding(7, 7).Gap(4).Children(func() {
			ui.Text(c, host).FontSize(12).Bold().MaxLines(1).Ellipsis("…")
			if detail != "" {
				ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
			}
		})
		a.sidebarConnection(c)
		a.sidebarHomeAssistant(c)
		label := "启动服务"
		if a.snapshot.Running {
			label = "停止服务"
		}
		button := ui.Button(c.Key("service-toggle"), "").Label(label).BorderWidth(0).Background(ui.Color{}).FillWidth().Shrink(0).Height(31).Padding(3, 7).Gap(9).Radius(7).AlignItems(ui.Center).Justify(ui.Start).Disabled(a.busy["service"])
		if button.Hovered() {
			button.Background(t.SurfaceHover)
		}
		button.Children(func() {
			ui.Icon(c, sidebarPower).Size(20, 20).TextColor(t.TextMuted)
			ui.Text(c, label).FontSize(12).Grow(1).SingleLine()
		}).OnClick(func() {
			work := a.service.Start
			if a.snapshot.Running {
				work = a.service.Stop
			}
			a.run("service", work, nil)
		})
	})
}
