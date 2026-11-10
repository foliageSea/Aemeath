package main

import (
	"strings"
	"testing"
)

func TestNativeConnectionDetailsStayInSidebarAndRecover(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, f, _, tt := nativeTest(t)
		tt.SetSize(760, 577)
		tt.SetDark(dark)
		f.state.Running = true
		f.state.Status = "reconnecting"
		f.state.Error = "device not found via mDNS (_aemeath._tcp) or aemeath.local " + strings.Repeat("请检查设备网络连接。", 30)
		a.refresh()
		nativeClick(t, tt, "语音助手")
		before, ok := tt.Find("对话记录")
		if !ok {
			t.Fatal("missing conversation")
		}
		button, ok := tt.Find("连接异常 · 查看详情")
		if !ok || button.X < 10 || button.X+button.W > 206 || button.Y+button.H > 565 {
			t.Fatalf("error entry outside sidebar: %+v", button)
		}
		nativeClick(t, tt, "连接异常 · 查看详情")
		after, _ := tt.Find("对话记录")
		if before != after {
			t.Fatalf("sidebar error displaced main content: before=%+v after=%+v", before, after)
		}
		detail, ok := tt.Find("设备连接错误详情")
		if !ok || detail.X < 10 || detail.X+detail.W > 206 || detail.H > 104 || detail.Y+detail.H > 565 {
			t.Fatalf("long connection error overflows sidebar: %+v", detail)
		}
		if !tt.HasText("设备连接：" + f.state.Error) {
			t.Fatal("connection diagnostics were lost")
		}
		for _, label := range []string{"停止服务", "概览", "订阅", "语音助手", "设置"} {
			r, ok := tt.Find(label)
			if !ok || r.X+r.W > 206 || r.Y < 0 || r.Y+r.H > 577 {
				t.Fatalf("connection details hid sidebar control %s: %+v", label, r)
			}
		}
		nativeClick(t, tt, "订阅")
		if _, ok := tt.Find("设备连接错误详情"); !ok {
			t.Fatal("page navigation lost sidebar diagnostics")
		}
		f.state.Connected = true
		f.state.Status = "connected"
		f.state.Error = ""
		a.refresh()
		tt.Frame()
		if a.details || tt.HasText("连接异常") || !tt.HasText("设备已连接") {
			t.Fatal("recovered connection kept stale warning")
		}
		f.state.Connected = false
		f.state.Status = "reconnecting"
		f.state.Error = "new connection failure"
		a.refresh()
		tt.Frame()
		if _, ok := tt.Find("设备连接错误详情"); ok {
			t.Fatal("new error unexpectedly reopened previous details")
		}
	}
}
