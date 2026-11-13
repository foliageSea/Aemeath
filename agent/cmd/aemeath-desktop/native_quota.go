package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/proto"
	"github.com/egoist/mygo/ui"
)

// saveSubscriptions commits preferences using the saved connection settings,
// never the user's unfinished connection draft. Failed attempts remain retryable.
func (a *nativeUI) saveSubscriptions() {
	if a.busy["save"] {
		return
	}
	settings := copySettings(a.snapshot.Settings)
	settings.Subscriptions = append([]desktop.Subscription{}, a.draft.Subscriptions...)
	a.quotaUnsaved = true
	a.run("save", func() error { return a.service.SaveSettings(settings) }, func() { a.quotaUnsaved = false; a.notify(nativeNoticeSuccess, "订阅已保存") })
}
func (a *nativeUI) toggleSubscription(name string) {
	if a.busy["save"] {
		return
	}
	list := a.subscriptions()
	for i := range list {
		if list[i].Name == name {
			list[i].Hidden = !list[i].Hidden
			a.draft.Subscriptions = list
			a.saveSubscriptions()
			return
		}
	}
}
func (a *nativeUI) reorderSubscriptions(rows []int, to int) {
	if a.busy["save"] {
		return
	}
	list := a.subscriptions()
	reordered := moveSubscriptions(list, rows, to)
	if slices.Equal(list, reordered) {
		return
	}
	a.draft.Subscriptions = reordered
	a.saveSubscriptions()
}

// to is an insertion boundary in the original list: zero for the beginning,
// len(list) for the end. Hidden and temporarily unavailable accounts participate.
func moveSubscriptions(list []desktop.Subscription, rows []int, to int) []desktop.Subscription {
	if to < 0 || to > len(list) {
		return list
	}
	selected := make(map[int]bool, len(rows))
	for _, row := range rows {
		if row < 0 || row >= len(list) {
			return list
		}
		selected[row] = true
	}
	var moving []desktop.Subscription
	for i, sub := range list {
		if selected[i] {
			moving = append(moving, sub)
		}
	}
	if len(moving) == 0 {
		return list
	}
	out := make([]desktop.Subscription, 0, len(list))
	for i := 0; i <= len(list); i++ {
		if i == to {
			out = append(out, moving...)
		}
		if i < len(list) && !selected[i] {
			out = append(out, list[i])
		}
	}
	return out
}

var (
	quotaEyeIcon    = sidebarSVG("<path d='M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8Z'/><circle cx='12' cy='12' r='3'/>")
	quotaEyeOffIcon = sidebarSVG("<path d='M3 3l18 18'/><path d='M10.58 10.58a2 2 0 0 0 2.83 2.83'/><path d='M9.88 4.24A9.12 9.12 0 0 1 12 4c7 0 10 7 10 7a13.3 13.3 0 0 1-3.17 4.1M6.61 6.61A13.5 13.5 0 0 0 2 11s3 7 10 7a9.26 9.26 0 0 0 4-.925'/>")
)

// quotaUsageSummary keeps each period readable without a long, wrapping sentence.
func quotaUsageSummary(c *ui.Context, u *proto.QuotaUsage) {
	t := c.Theme()
	ui.Grid(c.Key("quota-usage-summary")).FillWidth().Shrink(0).ColumnTracks(ui.Fr(1), ui.Fr(1)).Gap(12).Children(func() {
		for _, period := range []struct {
			key, label string
			calls      int64
			cost       float64
		}{
			{"today", "今日", u.CallsToday, u.CostToday},
			{"month", "近30天", u.Calls30d, u.Cost30d},
		} {
			ui.Column(c.Key("quota-usage-" + period.key)).Label(period.label + "用量").MinWidth(0).Padding(16).Gap(8).Radius(16).Background(t.Surface).Children(func() {
				ui.Text(c, period.label).FontSize(12).TextColor(t.TextMuted)
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, fmt.Sprintf("%d", period.calls)).FontSize(26).Bold().MinWidth(0).Wrap()
					ui.Text(c, "次调用").FontSize(11).TextColor(t.TextMuted).Shrink(0)
				})
				ui.Text(c, fmt.Sprintf("费用 · %.4f USD", period.cost)).FontSize(12).TextColor(t.TextMuted).Wrap()
			})
		}
	})
}

// joinNonEmpty joins the non-empty parts with sep, dropping a trailing
// separator when a part (such as an omitted balance or display string) is empty.
func joinNonEmpty(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}
func (a *nativeUI) quotaListView(c *ui.Context) {
	q := a.snapshot.Quota
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			if q != nil {
				label, color := "最近更新：", c.Theme().TextMuted
				if time.Now().Unix()-q.TS > 180 {
					label, color = "历史额度（已过期）：", c.Theme().Warning
				}
				ui.Text(c, label+nativeTime(q.TS)).FontSize(11).TextColor(color).Wrap()
			}
		})
		refreshLabel := "刷新额度"
		if a.busy["quota"] {
			refreshLabel = "刷新中…"
		}
		ui.Button(c.Key("refresh-quota"), refreshLabel).Shrink(0).Disabled(a.busy["quota"]).OnClick(func() { a.run("quota", a.service.RefreshQuotas, nil) })
		if a.quotaUnsaved && !a.busy["save"] {
			ui.Button(c.Key("retry-subscriptions"), "重试保存").Shrink(0).OnClick(a.saveSubscriptions)
		}
	})
	if a.snapshot.QuotaError != "" {
		ui.Text(c, "额度获取失败："+a.snapshot.QuotaError).TextColor(c.Theme().Danger).Wrap()
	}
	if q == nil {
		ui.Text(c, "暂无额度").TextColor(c.Theme().TextMuted)
	} else if q.Usage != nil {
		quotaUsageSummary(c, q.Usage)
	}
	list := a.subscriptions()
	a.quotaList.Key = func(i int) any { return list[i].Name }
	a.quotaList.Label = func(i int) string { return list[i].Name }
	// Keep list structure stable; action handlers reject requests while saving.
	a.quotaList.Reorder = a.reorderSubscriptions
	// Rows supplies the current scoped context even when virtual rows are built
	// later during layout; do not retain the outer build context in a row callback.
	// Right padding keeps the overlay scrollbar clear of the visibility
	// button at each card's trailing edge instead of covering it.
	ui.List(c.Key("subscription-list"), &a.quotaList, len(list)).Grow(1).MinHeight(0).Padding(0, 14, 0, 0).Gap(12).Label("订阅列表").Rows(func(row ui.ListRow) {
		a.quotaCard(row.Context, list[row.Index], a.snapshot.Quota)
	})
}
func (a *nativeUI) quotaCard(c *ui.Context, sub desktop.Subscription, q *proto.QuotaFrame) {
	t := c.Theme()
	ui.Column(c.Key(sub.Name)).FillWidth().Padding(18).Gap(10).Radius(18).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.ButtonBase(c.Key("drag-handle")).Label("拖动 "+sub.Name).Cursor(ui.CursorGrab).Size(24, 30).Children(func() { ui.Text(c, "⠿").FontSize(21).TextColor(t.TextMuted) })
			ui.Text(c, sub.Name).Bold().FontSize(17).Grow(1).MinWidth(0).Wrap()
			label, icon := "隐藏", quotaEyeIcon
			if sub.Hidden {
				label, icon = "恢复显示", quotaEyeOffIcon
			}
			visibility := ui.ButtonBase(c.Key("visibility")).Label(label+" "+sub.Name).Tooltip(label).
				Size(34, 34).Radius(10).Border(1, t.Border).Background(t.Surface)
			if visibility.Hovered() {
				visibility.Background(t.SurfaceHover)
			}
			visibility.Children(func() {
				ui.Icon(c, icon).FontSize(17).TextColor(t.TextMuted)
			}).OnClick(func() { a.toggleSubscription(sub.Name) })
		})
		if sub.Hidden {
			ui.Text(c, "已隐藏").FontSize(12).TextColor(t.TextMuted)
			return
		}
		found := false
		if q != nil {
			for _, account := range q.Accounts {
				if account.Name != sub.Name {
					continue
				}
				found = true
				// Balance is already formatted in its own currency by the gateway.
				ui.Text(c, joinNonEmpty(" · ", account.Plan, account.Kind, account.Balance)).Wrap()
				for _, window := range account.Windows {
					ui.Text(c, joinNonEmpty(" · ", window.Name, fmt.Sprintf("%.1f%%", window.Used), window.Display)).Wrap()
					nativeProgress(c, window.Used)
					if window.Reset > 0 {
						ui.Text(c, "重置于 "+nativeTime(window.Reset)).FontSize(12).TextColor(t.TextMuted)
					}
				}
			}
		}
		if !found {
			ui.Text(c, "暂无额度").TextColor(t.TextMuted)
		}
	})
}
