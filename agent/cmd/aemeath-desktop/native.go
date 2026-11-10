package main

import (
	"context"
	"fmt"
	"math"
	"time"

	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/hamcp"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type nativeService interface {
	Snapshot() desktop.State
	Start() error
	Stop() error
	SaveSettings(desktop.Settings) error
	SetPage(string) error
	SetScreen(bool, int) error
	RefreshQuotas() error
}

type nativeUI struct {
	service              nativeService
	snapshot             desktop.State
	draft                desktop.Settings
	page                 string
	notice               string
	noticePending        bool
	dirty                bool
	revision             int
	busy                 map[string]bool
	update               func(func()) // Host must assign win.Update before displaying the view.
	login                bool
	loginRead            func() bool
	loginWrite           func(bool) error
	history              []nativeSample
	details              bool
	quotaList            ui.ListState
	quotaUnsaved         bool
	voiceHold            bool
	voiceKeyDown         bool
	voiceGlobalReady     bool
	voiceGlobalKey       string
	voiceGlobalError     string
	voiceHoldSession     uint64
	voiceHoldStartTurn   uint32
	voicePreview         string
	visibleKeys          map[string]bool
	settingsTab          int
	voiceSettingsSection int
	voiceSettingsScroll  [6]ui.ScrollState
	voiceHistory         []nativeVoiceTurn
	voiceNextID          uint64
	voiceScroll          ui.ScrollState
	voiceCleared         bool
	voiceClearConfirm    bool
	voiceClearTurn       uint32
	voiceClearSession    uint64
	wakeAssets           nativeWakeAssets
	wakeAssetOps         nativeWakeAssetOps
	wakeAdvanced         bool
	homeAssistant        nativeHAState
	haProbe              func(context.Context, hamcp.Config) ([]hamcp.Tool, error)
}

func newNativeUI(service *desktop.Service) *nativeUI { return nativeState(service) }
func nativeState(service nativeService) *nativeUI {
	state := service.Snapshot()
	a := &nativeUI{service: service, snapshot: state, draft: copySettings(state.Settings), page: "概览", busy: map[string]bool{}, loginRead: mygo.App.OpenAtLogin, loginWrite: mygo.App.SetOpenAtLogin}
	a.restoreVoiceHistory()
	a.recordVoice()
	return a
}
func copySettings(s desktop.Settings) desktop.Settings {
	s.Subscriptions = append([]desktop.Subscription{}, s.Subscriptions...)
	return s
}

// refresh runs on the UI thread and never replaces the user's draft.
func (a *nativeUI) refresh() {
	a.snapshot = a.service.Snapshot()
	if a.snapshot.Error == "" {
		a.details = false
	}
	a.recordMetrics()
	a.recordVoice()
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
		a.quotaUnsaved = false
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
func (a *nativeUI) view(c *ui.Context) {
	// Once the new turn leaves listening, the backend has already ended the
	// hold (limit, mute, cancellation or error). Do not keep showing recording.
	if a.voiceHold && a.snapshot.Voice.Turn != a.voiceHoldStartTurn && a.snapshot.Voice.Phase != "listening" && a.snapshot.Voice.Phase != "wake" {
		a.voiceHold = false
	}
	if a.voiceHold && (!a.snapshot.Voice.Connected || a.snapshot.Voice.Session != a.voiceHoldSession) {
		a.endVoiceHold(true)
	}
	t := c.Theme()
	if a.noticePending {
		c.Toast(a.notice)
		a.noticePending = false
	}
	if a.voiceHold && c.Shortcut(0, ui.KeyEscape) {
		a.snapshot = a.service.Snapshot()
		a.endVoiceHold(true)
	}
	root := ui.Row(c.Key("app-root")).Fill().AlignItems(ui.Stretch).Background(t.Background)
	root.Children(func() {
		a.sidebar(c)
		ui.Column(c.Key("main")).Grow(1).MinWidth(0).MinHeight(0).Padding(20).Gap(14).Children(func() {
			a.header(c)
			if a.page == "订阅" {
				a.quotaListView(c)
				return
			}
			if a.page == "设置" {
				a.settingsView(c)
				return
			}
			if a.page == "语音助手" {
				a.voiceView(c)
				return
			}
			ui.Scroll(c.Key(a.page)).Grow(1).Children(func() {
				ui.Column(c).FillWidth().Gap(16).Children(func() {
					a.overview(c)
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
func (a *nativeUI) settingsView(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Tabs(c.Key("settings-tabs"), &a.settingsTab, "连接", "通用", "屏幕", "语音", "智能家居").Grow(1)
		if a.dirty {
			ui.Text(c, "未保存").FontSize(12).TextColor(c.Theme().Warning)
		}
		ui.PrimaryButton(c.Key("save-settings"), "保存设置").Disabled(a.busy["save"]).OnClick(a.save)
	})
	if a.settingsTab == 3 {
		a.voiceSettingsView(c)
		return
	}
	ui.Scroll(c.Key(fmt.Sprintf("settings-tab-%d", a.settingsTab))).Grow(1).Children(func() {
		ui.Column(c).FillWidth().Gap(16).Children(func() {
			switch a.settingsTab {
			case 1:
				a.generalSettingsView(c)
			case 2:
				a.screenSettingsView(c)
			case 4:
				a.homeAssistantSettingsView(c)
			default:
				a.connectionSettingsView(c)
			}
		})
	})
}

func (a *nativeUI) connectionSettingsView(c *ui.Context) {
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
	})
}

func (a *nativeUI) generalSettingsView(c *ui.Context) {
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

func (a *nativeUI) screenSettingsView(c *ui.Context) {
	ui.Column(c.Key("screen-card")).FillWidth().Padding(20).Gap(12).Radius(18).Background(c.Theme().Surface).Children(func() {
		ui.Text(c, "设备屏幕").Bold().FontSize(17)
		// Controls wait for the device's own report: it is the source of truth.
		screenDisabled := !a.snapshot.Connected || !a.snapshot.ScreenKnown || a.busy["screen"]
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "屏幕状态")
			screenOn := a.snapshot.ScreenOn
			if ui.Switch(c.Key("screen-on"), &screenOn).Label("亮屏").Disabled(screenDisabled).Changed() {
				brightness := a.snapshot.Brightness
				a.run("screen", func() error { return a.service.SetScreen(screenOn, brightness) }, func() { a.notify("屏幕指令已发送") })
			}
		})
		ui.Text(c, "亮度")
		brightness := float64(a.snapshot.Brightness)
		if ui.Slider(c.Key("screen-brightness"), &brightness, 0, 100).Label("亮度").Disabled(screenDisabled || !a.snapshot.ScreenOn).FillWidth().Changed() {
			on, level := a.snapshot.ScreenOn, int(brightness)
			a.run("screen", func() error { return a.service.SetScreen(on, level) }, func() { a.notify("亮度指令已发送") })
		}
	})
}
