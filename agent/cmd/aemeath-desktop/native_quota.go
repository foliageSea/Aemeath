package main

import (
	"fmt"
	"slices"
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
	a.run("save", func() error { return a.service.SaveSettings(settings) }, func() { a.quotaUnsaved = false; a.notify("订阅已保存") })
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
func quotaUsageText(u *proto.QuotaUsage) string {
	return fmt.Sprintf("今日 %d 次 / %.4f USD · 近30天 %d 次 / %.4f USD", u.CallsToday, u.CostToday, u.Calls30d, u.Cost30d)
}
func (a *nativeUI) quotaListView(c *ui.Context) {
	ui.Row(c).Gap(10).Children(func() {
		ui.Button(c.Key("refresh-quota"), "刷新额度").Disabled(a.busy["quota"]).OnClick(func() { a.run("quota", a.service.RefreshQuotas, nil) })
		if a.quotaUnsaved && !a.busy["save"] {
			ui.Button(c.Key("retry-subscriptions"), "重试保存").OnClick(a.saveSubscriptions)
		}
	})
	q := a.snapshot.Quota
	if a.snapshot.QuotaError != "" {
		ui.Text(c, "额度获取失败："+a.snapshot.QuotaError).TextColor(c.Theme().Danger).Wrap()
	}
	if q == nil {
		ui.Text(c, "暂无额度").TextColor(c.Theme().TextMuted)
	} else {
		if q.Usage != nil {
			ui.Text(c, quotaUsageText(q.Usage)).Wrap()
		}
		label := "最近更新："
		if time.Now().Unix()-q.TS > 180 {
			label = "历史额度（已过期）："
		}
		ui.Text(c, label+nativeTime(q.TS)).FontSize(12).TextColor(c.Theme().TextMuted)
	}
	list := a.subscriptions()
	a.quotaList.Key = func(i int) any { return list[i].Name }
	a.quotaList.Label = func(i int) string { return list[i].Name }
	// Keep list structure stable; action handlers reject requests while saving.
	a.quotaList.Reorder = a.reorderSubscriptions
	// Rows supplies the current scoped context even when virtual rows are built
	// later during layout; do not retain the outer build context in a row callback.
	ui.List(c.Key("subscription-list"), &a.quotaList, len(list)).Grow(1).MinHeight(0).Gap(12).Label("订阅列表").Rows(func(row ui.ListRow) {
		a.quotaCard(row.Context, list[row.Index], a.snapshot.Quota)
	})
}
func (a *nativeUI) quotaCard(c *ui.Context, sub desktop.Subscription, q *proto.QuotaFrame) {
	t := c.Theme()
	ui.Column(c.Key(sub.Name)).FillWidth().Padding(18).Gap(10).Radius(18).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.ButtonBase(c.Key("drag-handle")).Label("拖动 "+sub.Name).Cursor(ui.CursorGrab).Size(24, 30).Children(func() { ui.Text(c, "⠿").FontSize(21).TextColor(t.TextMuted) })
			ui.Text(c, sub.Name).Bold().FontSize(17).Grow(1).MinWidth(0).Wrap()
			label := "隐藏"
			if sub.Hidden {
				label = "恢复显示"
			}
			ui.Button(c.Key("visibility"), label).Label(label + " " + sub.Name).OnClick(func() { a.toggleSubscription(sub.Name) })
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
				ui.Text(c, account.Plan+" · "+account.Kind+" · "+account.Balance).Wrap()
				for _, window := range account.Windows {
					ui.Text(c, fmt.Sprintf("%s · %.1f%% · %s", window.Name, window.Used, window.Display)).Wrap()
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
