package main

import (
	"fmt"
	"net/url"

	"aemeath/agent/internal/brain"
	"github.com/egoist/mygo/ui"
)

func (a *nativeUI) voiceWebSearchSourcesView(c *ui.Context, report brain.WebSearchReport) {
	t := c.Theme()
	ui.Text(c, report.Summary()).FontSize(11).TextColor(t.TextMuted).Wrap()
	for i, source := range report.Results {
		ui.Column(c.Key(fmt.Sprintf("web-source-%d", i))).FillWidth().MinWidth(0).Padding(10).Gap(5).Radius(8).Border(1, t.Border.Alpha(0.5)).Children(func() {
			ui.Link(c, fmt.Sprintf("[%d] %s", i+1, source.Title), source.URL).Label(fmt.Sprintf("网页来源 %d：%s", i+1, source.Title)).FontSize(12).Wrap()
			parsed, _ := url.Parse(source.URL)
			if parsed != nil {
				ui.Text(c, parsed.Hostname()).FontSize(10).TextColor(t.TextMuted)
			}
			if source.Snippet != "" {
				ui.Text(c, source.Snippet).FontSize(11).TextColor(t.TextMuted).Wrap().Selectable()
			}
		})
	}
}
