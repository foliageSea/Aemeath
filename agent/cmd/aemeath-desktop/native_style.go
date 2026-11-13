package main

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

type nativeSample struct {
	ts     int64
	values [5]float64
}

func metricValue(p *float64) float64 {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) {
		return math.NaN()
	}
	return *p
}
func (a *nativeUI) recordMetrics() {
	m := a.snapshot.Metrics
	if m == nil || m.TS <= 0 || len(a.history) > 0 && m.TS <= a.history[len(a.history)-1].ts {
		return
	}
	sample := nativeSample{ts: m.TS, values: [5]float64{math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN()}}
	if m.CPU != nil {
		sample.values[0] = metricValue(m.CPU.Total)
	}
	if m.Mem != nil && m.Mem.Total > 0 {
		sample.values[1] = float64(m.Mem.Used) / float64(m.Mem.Total) * 100
	}
	if m.GPU != nil {
		sample.values[2] = metricValue(m.GPU.Load)
	}
	if m.Disk != nil && m.Disk.Read != nil && m.Disk.Write != nil {
		sample.values[3] = metricValue(m.Disk.Read) + metricValue(m.Disk.Write)
	}
	if m.Net != nil {
		sample.values[4] = metricValue(m.Net.Down)
	}
	a.history = append(a.history, sample)
	if len(a.history) > 60 {
		a.history = append([]nativeSample(nil), a.history[len(a.history)-60:]...)
	}
}

var nativePageNames = []string{"metrics", "quota", "voice"}

var (
	screenMonitorIcon    = sidebarSVG("<rect x='2' y='3' width='20' height='14' rx='2'/><path d='M8 21h8M12 17v4'/>")
	screenMonitorOffIcon = sidebarSVG("<path d='M10.5 3H20a2 2 0 0 1 2 2v8.5'/><path d='M4.7 3.3A2 2 0 0 0 2 5v10a2 2 0 0 0 2 2h13'/><path d='M8 21h8M12 17v4'/><path d='M2 2l20 20'/>")
)

func (a *nativeUI) header(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, a.page).FontSize(25).Bold().Grow(1).MinWidth(0)
		pageIndex := 0
		for i, page := range nativePageNames {
			if a.snapshot.Page == page {
				pageIndex = i
			}
		}
		seg := ui.Segmented(c, &pageIndex, "性能屏", "额度屏", "语音屏").FontSize(12).Disabled(!a.snapshot.Connected || a.busy["page"])
		if seg.Changed() {
			page := nativePageNames[pageIndex]
			a.run("page", func() error { return a.service.SetPage(page) }, func() { a.notify(nativeNoticeSuccess, "切页指令已发送") })
		}
		screenLabel, screenIcon := "息屏", screenMonitorIcon
		if !a.snapshot.ScreenOn {
			screenLabel, screenIcon = "亮屏", screenMonitorOffIcon
		}
		disabled := !a.snapshot.Connected || !a.snapshot.ScreenKnown || a.busy["screen"]
		toggle := ui.ButtonBase(c.Key("screen-toggle")).Label(screenLabel).Tooltip(screenLabel).Size(34, 34).Radius(10).Border(1, t.Border).Background(t.Surface).Disabled(disabled)
		if !disabled && toggle.Hovered() {
			toggle.Background(t.SurfaceHover)
		}
		iconColor := t.TextMuted
		if disabled {
			iconColor = t.TextMuted.Alpha(0.5)
		}
		toggle.Children(func() {
			ui.Icon(c, screenIcon).FontSize(17).TextColor(iconColor)
		}).OnClick(func() {
			on, brightness := !a.snapshot.ScreenOn, a.snapshot.Brightness
			a.run("screen", func() error { return a.service.SetScreen(on, brightness) }, func() { a.notify(nativeNoticeSuccess, "屏幕指令已发送") })
		})
	})
}
func metricColor(index int) ui.Color {
	return []ui.Color{ui.Hex("#3b92ee"), ui.Hex("#35b569"), ui.Hex("#ec729b"), ui.Hex("#e9a03e"), ui.Hex("#37b2b8")}[index]
}
func nativeRate(v *float64) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "—"
	}
	value := *v
	unit := "B/s"
	for _, next := range []string{"KB/s", "MB/s", "GB/s"} {
		if math.Abs(value) < 1024 {
			break
		}
		value /= 1024
		unit = next
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

type nativeMetricCard struct{ title, value, subtitle, detail string }

func (a *nativeUI) metricCards() []nativeMetricCard {
	cards := []nativeMetricCard{{title: "CPU", value: "—", subtitle: "当前使用率", detail: "处理器数据不可用"}, {title: "内存", value: "—", subtitle: "当前已用", detail: "内存数据不可用"}, {title: "GPU", value: "—", subtitle: "当前使用率", detail: "GPU 数据不可用"}, {title: "磁盘", value: "—", subtitle: "读写吞吐量", detail: "磁盘数据不可用"}, {title: "网络", value: "—", subtitle: "当前下载", detail: "网络数据不可用"}}
	m := a.snapshot.Metrics
	if m == nil {
		return cards
	}
	if m.CPU != nil {
		cards[0].value = nativeNumber(m.CPU.Total, "%")
		cards[0].subtitle = "温度 " + nativeNumber(m.CPU.Temp, "°C") + " · " + nativeNumber(m.CPU.Freq, " MHz")
		cards[0].detail = "CPU · " + m.CPU.Name
	}
	if m.Mem != nil {
		cards[1].value = nativeBytes(m.Mem.Used)
		cards[1].subtitle = "总计 " + nativeBytes(m.Mem.Total)
		cards[1].detail = "内存 · " + nativeBytes(m.Mem.Used) + " / " + nativeBytes(m.Mem.Total)
	}
	if m.GPU != nil {
		cards[2].value = nativeNumber(m.GPU.Load, "%")
		cards[2].subtitle = "温度 " + nativeNumber(m.GPU.Temp, "°C")
		cards[2].detail = "GPU · " + m.GPU.Name
	}
	if m.Disk != nil {
		if m.Disk.Read != nil && m.Disk.Write != nil {
			sum := *m.Disk.Read + *m.Disk.Write
			cards[3].value = nativeRate(&sum)
		}
		cards[3].subtitle = "读 " + nativeRate(m.Disk.Read) + " · 写 " + nativeRate(m.Disk.Write)
		if m.Disk.Used != nil && m.Disk.Total != nil {
			cards[3].detail = "已用 " + nativeBytes(*m.Disk.Used) + " / " + nativeBytes(*m.Disk.Total)
		}
	}
	if m.Net != nil {
		cards[4].value = nativeRate(m.Net.Down)
		cards[4].subtitle = "上传 " + nativeRate(m.Net.Up)
		cards[4].detail = m.Net.IP
	}
	return cards
}
func metricColumns(width float32) int {
	if width >= 1000 {
		return 3
	}
	return 2
}
func (a *nativeUI) metricGrid(c *ui.Context) {
	t := c.Theme()
	width, _ := c.Size()
	columns := metricColumns(width)
	if a.snapshot.Metrics == nil {
		ui.Text(c, "等待本机首次采样…").TextColor(t.TextMuted)
	}
	tracks := make([]ui.Track, columns)
	for i := range tracks {
		tracks[i] = ui.Fr(1)
	}
	cards := a.metricCards()
	ui.Grid(c.Key("metric-grid")).FillWidth().ColumnTracks(tracks...).Gap(14).Children(func() {
		for index, card := range cards {
			color := metricColor(index)
			ui.Column(c.Key("metric-" + card.title)).Label("指标卡 " + card.title).MinWidth(0).Padding(18).Gap(9).Radius(20).Background(t.Surface).Children(func() {
				ui.Text(c, card.title).Bold().FontSize(16).TextColor(color)
				ui.Text(c, card.value).Bold().FontSize(31).MaxLines(1).Ellipsis("…")
				ui.Text(c, card.subtitle).FontSize(11).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
				ui.Text(c, card.detail).FontSize(11).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
				a.sparkline(c, index, color)
			})
		}
		ui.Column(c.Key("device-card")).MinWidth(0).Padding(18).Gap(10).Radius(20).Background(t.Surface).Children(func() {
			ui.Text(c, "设备推送").Bold().FontSize(16).TextColor(t.Accent)
			label := "已暂停"
			if a.snapshot.Running {
				label = "等待连接"
			}
			if a.snapshot.Connected {
				label = "推送中"
			}
			ui.Text(c, label).FontSize(29).Bold()
			ui.Text(c, nativeStatus(a.snapshot.Status)).FontSize(12).TextColor(t.TextMuted)
			ui.Text(c, a.snapshot.Target).FontSize(11).TextColor(t.TextMuted).MaxLines(1).Ellipsis("…")
			ui.Text(c, "最近发送 · "+nativeTime(a.snapshot.LastSent)).FontSize(10).TextColor(t.TextMuted).Wrap()
		})
	})
}
func (a *nativeUI) sparkline(c *ui.Context, index int, color ui.Color) {
	samples := append([]nativeSample(nil), a.history...)
	valid := 0
	ceiling := 100.0
	for _, s := range samples {
		if v := s.values[index]; !math.IsNaN(v) && !math.IsInf(v, 0) {
			valid++
			if index >= 3 {
				ceiling = math.Max(ceiling, v*1.1)
			}
		}
	}
	if valid == 0 {
		ui.Box(c).FillWidth().Height(46).Radius(10).Background(color.Alpha(0.07)).Children(func() { ui.Text(c, "暂无历史数据").FontSize(10).TextColor(c.Theme().TextMuted).Padding(12) })
		return
	}
	ui.Box(c).FillWidth().Height(46).Radius(10).Background(color.Alpha(0.07)).Label("趋势 " + []string{"CPU", "内存", "GPU", "磁盘", "网络"}[index]).Draw(func(p *ui.Painter, r ui.Rect) {
		first, last := samples[0].ts, samples[len(samples)-1].ts
		point := func(s nativeSample) (float32, float32) {
			return r.X + 4 + (r.W-8)*float32(s.ts-first)/float32(max(int64(1), last-first)), r.Y + r.H - 4 - (r.H-8)*float32(math.Max(0, math.Min(1, s.values[index]/ceiling)))
		}
		segment := make([][2]float32, 0, len(samples))
		flush := func() {
			if len(segment) == 1 {
				pt := segment[0]
				p.Fill(ui.Rect{X: pt[0] - 0.9, Y: pt[1] - 0.9, W: 1.8, H: 1.8}, color, 0.9)
			} else if len(segment) > 1 {
				p.StrokePolyline(segment, 1.8, color)
			}
			segment = segment[:0]
		}
		for i, s := range samples {
			if i > 0 && s.ts-samples[i-1].ts > 2 {
				flush()
			}
			if math.IsNaN(s.values[index]) || math.IsInf(s.values[index], 0) {
				flush()
				continue
			}
			x, y := point(s)
			segment = append(segment, [2]float32{x, y})
		}
		flush()
	})
}
