//go:build e2e

package main

import (
	"log"
	"os"
	"time"

	"aemeath/agent/internal/desktop"
	"aemeath/agent/internal/pet"
	"github.com/egoist/mygo"
)

// Isolated visual fixtures never call ASR, LLM, TTS, a microphone or a device.
// They are opt-in even in e2e builds and are entirely excluded from releases.
func init() {
	if os.Getenv("AEMEATH_TEST_PET_SCENARIOS") != "1" {
		return
	}
	nativePetTestSetup = func(p *nativePet) {
		s := desktop.State{}
		s.Settings.Voice.Enabled = true
		s.Voice.Connected = true
		s.Voice.Session = 1
		s.Voice.Phase = "idle"
		p.snapshot = func() desktop.State { return s }
		p.command = func(kind, text string) error { log.Printf("pet fixture command: %s", kind); return nil }
		p.refreshAssistant(time.Now())
		var items []*mygo.MenuItem
		action := func(label string, fn func()) {
			items = append(items, &mygo.MenuItem{Label: label, Click: func(*mygo.MenuItem, *mygo.Window) { fn() }})
		}
		phase := func(label, value string) {
			action(label, func() { s.Voice.Turn++; s.Voice.Phase = value; s.Voice.Response = ""; p.refreshAssistant(time.Now()) })
		}
		phase("聆听", "listening")
		phase("思考", "thinking")
		phase("合成", "synthesizing")
		phase("播报", "speaking")
		phase("错误", "error")
		action("回复", func() {
			s.Voice.Response = "今天也一起慢慢来吧。忙完记得休息，我会在这里陪你。"
			p.refreshAssistant(time.Now())
		})
		action("长回复", func() {
			s.Voice.Response = "这是一段用于验证对话气泡自动换行、Unicode截断和完整对话入口的较长回复。角色窗口与气泡窗口是分开的，所以气泡隐藏时不会留下一个很大的透明区域遮挡桌面。详细内容可以在主界面查看。"
			p.refreshAssistant(time.Now())
		})
		action("待机", func() { s.Voice.Phase = "idle"; s.Voice.Response = ""; p.refreshAssistant(time.Now()) })
		action("断开", func() { s.Voice.Connected = false; p.refreshAssistant(time.Now()) })
		action("恢复连接", func() {
			s.Voice.Connected = true
			s.Voice.Session++
			s.Voice.Turn = 0
			s.Voice.Phase = "idle"
			p.refreshAssistant(time.Now())
		})
		action("隐藏", func() { p.setVisible(false) })
		action("恢复", p.recover)
		action("暂停", func() { p.setPaused(true) })
		action("恢复活动", func() { p.setPaused(false) })
		action("穿透", func() { p.setIgnoreMouse(true) })
		action("放大", func() { p.changeConfig(func(c *pet.Config) { c.Scale = 1.5 }) })
		action("正常大小", func() { p.changeConfig(func(c *pet.Config) { c.Scale = 1 }) })
		action("置顶", func() { p.changeConfig(func(c *pet.Config) { c.AlwaysOnTop = true }) })
		action("取消置顶", func() { p.changeConfig(func(c *pet.Config) { c.AlwaysOnTop = false }) })
		action("取消请求", p.cancelVoice)
		action("退出", mygo.App.Quit)
		mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{{Label: "桌宠验收", Submenu: items}}))
	}
}
