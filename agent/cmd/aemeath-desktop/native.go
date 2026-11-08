package main

import (
	"fmt"
	"math"
	"time"

	"aemeath/agent/internal/desktop"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type nativeService interface {
	Snapshot() desktop.State
	Start() error
	Stop() error
	SaveSettings(desktop.Settings) error
	SetPage(string) error
	RefreshQuotas() error
}

type nativeUI struct {
	service       nativeService
	snapshot      desktop.State
	draft         desktop.Settings
	page          string
	notice        string
	noticePending bool
	dirty         bool
	revision      int
	busy          map[string]bool
	update        func(func()) // Host must assign win.Update before displaying the view.
	login         bool
	loginRead     func() bool
	loginWrite    func(bool) error
	history       []nativeSample
	details       bool
}

func newNativeUI(service *desktop.Service) *nativeUI { return nativeState(service) }
func nativeState(service nativeService) *nativeUI {
	state := service.Snapshot()
	return &nativeUI{service: service, snapshot: state, draft: copySettings(state.Settings), page: "概览", busy: map[string]bool{}, loginRead: mygo.App.OpenAtLogin, loginWrite: mygo.App.SetOpenAtLogin}
}
func copySettings(s desktop.Settings) desktop.Settings {
	s.Subscriptions = append([]desktop.Subscription{}, s.Subscriptions...)
	return s
}

// refresh runs on the UI thread and never replaces the user's draft.
func (a *nativeUI) refresh() {
	a.snapshot = a.service.Snapshot()
	a.recordMetrics()
	if !a.busy["login"] {
		a.login = a.loginRead()
	}
}
func (a *nativeUI) notify(message string) {
	a.notice = message
	a.noticePending = true
}

func (a *nativeUI) run(key string, work func() error, done func()) {
	if a.busy[key] {
		return
	}
	dispatch := a.update
	if dispatch == nil {
		a.notify("窗口尚未就绪，请稍后重试。")
		return
	}
	a.busy[key] = true
	go func() {
		err := work()
		dispatch(func() {
			a.busy[key] = false
			if err != nil {
				a.notify("操作失败，修改已保留：" + err.Error())
			} else {
				a.notify("操作已完成。")
				if done != nil {
					done()
				}
			}
			a.refresh()
		})
	}()
}
func (a *nativeUI) edited() { a.dirty = true; a.revision++ }
func (a *nativeUI) save() {
	settings, revision := copySettings(a.draft), a.revision
	a.run("save", func() error { return a.service.SaveSettings(settings) }, func() {
		if a.revision == revision {
			a.dirty = false
		}
		a.notify("设置已保存。")
	})
}
func (a *nativeUI) subscriptions() []desktop.Subscription {
	list := append([]desktop.Subscription{}, a.draft.Subscriptions...)
	seen := map[string]bool{}
	for _, s := range list {
		seen[s.Name] = true
	}
	if a.snapshot.Quota != nil {
		for _, account := range a.snapshot.Quota.Accounts {
			if !seen[account.Name] {
				list = append(list, desktop.Subscription{Name: account.Name})
				seen[account.Name] = true
			}
		}
	}
	return list
}
func (a *nativeUI) changeSubscription(name string, move int, toggle bool) {
	if a.busy["save"] {
		return
	}
	list := a.subscriptions()
	for i := range list {
		if list[i].Name == name {
			if toggle {
				list[i].Hidden = !list[i].Hidden
			} else {
				j := i + move
				if j < 0 || j >= len(list) {
					return
				}
				list[i], list[j] = list[j], list[i]
			}
			a.draft.Subscriptions = list
			// Ordering must not implicitly persist unfinished address/gateway edits.
			settings := copySettings(a.snapshot.Settings)
			settings.Subscriptions = append([]desktop.Subscription{}, list...)
			a.run("save", func() error { return a.service.SaveSettings(settings) }, func() { a.notify("订阅顺序与显示状态已保存。") })
			return
		}
	}
}
func (a *nativeUI) view(c *ui.Context) {
	t := c.Theme()
	if a.noticePending {
		c.Toast(a.notice)
		a.noticePending = false
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Background(t.Background).Children(func() {
		a.sidebar(c)
		ui.Column(c.Key("main")).Grow(1).MinWidth(0).Padding(20).Gap(14).Children(func() {
			a.header(c)
			if a.snapshot.Error != "" {
				ui.Button(c.Key("connection-details"), "连接异常 · 查看详情").TextColor(t.Warning).OnClick(func() { a.details = !a.details })
				if a.details {
					ui.Text(c, "设备连接："+a.snapshot.Error).FontSize(12).TextColor(t.TextMuted).Wrap()
				}
			}
			ui.Scroll(c.Key(a.page)).Grow(1).Children(func() {
				ui.Column(c).FillWidth().Gap(16).Children(func() {
					switch a.page {
					case "订阅":
						a.quotaView(c)
					case "设置":
						a.settingsView(c)
					default:
						a.overview(c)
					}
				})
			})
		})
	})
}
func nativeStatus(status string) string {
	labels := map[string]string{"stopped": "推送已停止", "connecting": "正在连接", "discovering": "正在发现设备", "connected": "设备已连接", "reconnecting": "等待重新连接", "closed": "服务已关闭"}
	if label, ok := labels[status]; ok {
		return label
	}
	return status
}

func nativeTime(ts int64) string {
	if ts <= 0 {
		return "—"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}
func nativeNumber(v *float64, suffix string) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "—"
	}
	return fmt.Sprintf("%.1f%s", *v, suffix)
}
func nativeBytes(v uint64) string { return fmt.Sprintf("%.1f GB", float64(v)/(1<<30)) }
func nativeProgress(c *ui.Context, v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		v = 0
	}
	ui.Progress(c, math.Max(0, math.Min(1, v/100))).FillWidth()
}
func (a *nativeUI) overview(c *ui.Context) { a.metricGrid(c) }
func (a *nativeUI) quotaView(c *ui.Context) {
	ui.Button(c, "刷新额度").Disabled(a.busy["quota"]).OnClick(func() { a.run("quota", a.service.RefreshQuotas, nil) })
	q := a.snapshot.Quota
	if a.snapshot.QuotaError != "" {
		ui.Text(c, "额度获取失败："+a.snapshot.QuotaError).Wrap()
	}
	if q == nil {
		ui.Text(c, "暂无额度")
	} else {
		label := "最近更新："
		if time.Now().Unix()-q.TS > 180 {
			label = "历史额度（已过期）："
		}
		ui.Text(c, label+nativeTime(q.TS))
		if q.Usage != nil {
			ui.Text(c, fmt.Sprintf("今日 %d 次 / %.4f · 近30天 %d 次 / %.4f", q.Usage.CallsToday, q.Usage.CostToday, q.Usage.Calls30d, q.Usage.Cost30d)).Wrap()
		}
	}
	list := a.subscriptions()
	for i, sub := range list {
		ui.Column(c.Key(sub.Name)).FillWidth().Padding(18).Gap(10).Radius(18).Background(c.Theme().Surface).Children(func() {
			ui.Text(c, sub.Name).Bold().FontSize(17).Wrap()
			ui.Row(c).Gap(8).Children(func() {
				ui.Button(c, "上移").Label("上移 " + sub.Name).Disabled(i == 0 || a.busy["save"]).OnClick(func() { a.changeSubscription(sub.Name, -1, false) })
				ui.Button(c, "下移").Label("下移 " + sub.Name).Disabled(i == len(list)-1 || a.busy["save"]).OnClick(func() { a.changeSubscription(sub.Name, 1, false) })
				label := "隐藏"
				if sub.Hidden {
					label = "恢复显示"
				}
				ui.Button(c, label).Label(label + " " + sub.Name).Disabled(a.busy["save"]).OnClick(func() { a.changeSubscription(sub.Name, 0, true) })
			})
			if sub.Hidden {
				ui.Text(c, "已隐藏").FontSize(12)
				return
			}
			found := false
			if q != nil {
				for _, account := range q.Accounts {
					if account.Name != sub.Name {
						continue
					}
					found = true
					ui.Text(c, account.Plan+" · "+account.Kind+" · "+account.Balance).Wrap()
					for _, window := range account.Windows {
						ui.Text(c, fmt.Sprintf("%s · %.1f%% · %s", window.Name, window.Used, window.Display)).Wrap()
						nativeProgress(c, window.Used)
						if window.Reset > 0 {
							ui.Text(c, "重置于 "+nativeTime(window.Reset)).FontSize(12)
						}
					}
				}
			}
			if !found {
				ui.Text(c, "暂无额度").Wrap()
			}
		})
	}
}
func (a *nativeUI) settingsView(c *ui.Context) {
	ui.Column(c.Key("connection-card")).FillWidth().Padding(20).Gap(12).Radius(18).Background(c.Theme().Surface).Children(func() {
		ui.Text(c, "连接配置").Bold().FontSize(17)
		ui.Text(c, "设备").Bold()
		if ui.TextInput(c.Key("address"), &a.draft.Address).Label("设备地址").Placeholder("aemeath.local 或 192.168.1.20:80").FillWidth().Changed() {
			a.edited()
		}
		ui.Text(c, "Magpie 网关").Bold()
		if ui.TextInput(c.Key("magpie-url"), &a.draft.MagpieURL).Label("Magpie 网关").Placeholder("http://127.0.0.1:3425").FillWidth().Changed() {
			a.edited()
		}
		ui.PrimaryButton(c.Key("save-settings"), "保存设置").Disabled(a.busy["save"]).OnClick(a.save)
		if a.dirty {
			ui.Text(c, "未保存").Wrap()
		}

	})
	ui.Column(c.Key("login-card")).FillWidth().Padding(20).Gap(12).Radius(18).Background(c.Theme().Surface).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "开机启动")
			enabled := a.login
			if ui.Switch(c.Key("open-at-login"), &enabled).Label("登录系统时启动").Disabled(a.busy["login"]).Changed() {
				a.run("login", func() error { return a.loginWrite(enabled) }, func() { a.login = enabled })
			}
		})
	})
}
