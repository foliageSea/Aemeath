package main

import (
	"aemeath/agent/internal/brain"
	"github.com/egoist/mygo/ui"
)

func (a *nativeUI) voiceCurrentTimeView(c *ui.Context, report brain.CurrentTimeReport) {
	t, info := c.Theme(), report.Info
	ui.Column(c.Key("current-time-result")).FillWidth().MinWidth(0).Padding(12).Gap(6).Radius(10).Border(1, t.Border.Alpha(0.6)).Children(func() {
		ui.Text(c, "查询时刻").FontSize(10).TextColor(t.TextMuted)
		ui.Text(c, info.Time).FontSize(24).Bold().Selectable()
		ui.Text(c, info.Date+" · "+info.Weekday).FontSize(12)
		zone := info.Timezone
		if zone == "Local" {
			zone = "本机时区"
		}
		zone += " · UTC" + info.UTCOffset
		if info.IsDST {
			zone += " · 夏令时"
		}
		ui.Text(c, zone).FontSize(11).TextColor(t.TextMuted).Wrap()
		if info.Source == "network" {
			ui.Text(c, "外部时间 · "+info.Provider).FontSize(11).TextColor(t.Success).Wrap()
		} else {
			ui.Text(c, info.Notice).FontSize(11).TextColor(t.Warning).Wrap()
		}
		ui.Text(c, "UTC："+info.UTC).FontSize(10).TextColor(t.TextMuted).Wrap().Selectable()
	})
}
