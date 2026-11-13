package main

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestNativePageFeedbackUsesTransientToastWithoutMovingContent(t *testing.T) {
	a, f, updates, tt := nativeTest(t)
	f.mu.Lock()
	f.state.Running = true
	f.state.Connected = true
	f.state.Status = "connected"
	f.mu.Unlock()
	a.refresh()
	tt.Frame()
	before, ok := tt.Find("指标卡 CPU")
	if !ok {
		t.Fatal("CPU card missing")
	}
	show := func(label string) {
		t.Helper()
		nativeClick(t, tt, label)
		nativeComplete(t, updates, tt)
		tt.Advance(250 * time.Millisecond)
		if !tt.HasText("切页指令已发送") {
			t.Fatal("page feedback toast missing")
		}
		toast, ok := tt.Find("切页指令已发送")
		if !ok || toast.Y < float32(nativeTitleBarHeight) || toast.Y > 150 {
			t.Fatalf("feedback is not floating near the top: %+v", toast)
		}
		box, ok := tt.Find("成功提醒")
		if !ok || math.Abs(float64(box.X+box.W/2-550)) > 1 {
			t.Fatalf("feedback is not horizontally centered: %+v", box)
		}
		after, _ := tt.Find("指标卡 CPU")
		if after != before {
			t.Fatalf("toast moved content: before=%+v after=%+v", before, after)
		}
	}
	show("额度屏")
	tt.Advance(5 * time.Second)
	if tt.HasText("切页指令已发送") {
		t.Fatal("toast did not expire")
	}
	for range 3 {
		a.refresh()
		tt.Frame()
		tt.Advance(time.Second)
	}
	if tt.HasText("切页指令已发送") {
		t.Fatal("refresh redisplayed an expired toast")
	}
	show("性能屏")
}

func TestNativeToastTypesAndDismiss(t *testing.T) {
	for _, tc := range []struct {
		kind  nativeNoticeType
		label string
	}{
		{nativeNoticeSuccess, "成功"},
		{nativeNoticeError, "错误"},
		{nativeNoticeWarning, "警告"},
		{nativeNoticeInfo, "信息"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			a, _, _, tt := nativeTest(t)
			a.notify(tc.kind, "测试提醒")
			tt.Frame()
			for _, text := range tt.Texts() {
				if text == tc.label {
					t.Fatalf("type still rendered as text: %q", text)
				}
			}
			if _, ok := tt.Find(tc.label + "图标"); !ok || !tt.HasText("测试提醒") {
				t.Fatalf("missing typed toast: %v", tt.Texts())
			}
			nativeClick(t, tt, "关闭提醒")
			if tt.HasText("测试提醒") {
				t.Fatal("dismissed toast is still visible")
			}
		})
	}
}

func TestNativeToastWrapsWithinNarrowWindow(t *testing.T) {
	a, _, _, _ := nativeTest(t)
	tt := ui.NewTester(func(c *ui.Context) {
		c.AddToast(ui.Toast{ID: "long", Title: "这是一条很长的错误提醒，用于确认较窄窗口中的提示内容能够自动换行，不会超出窗口或遮住关闭按钮。", Type: "error"})
		a.toastView(c)
	}, 360, 600)
	tt.Frame()
	box, ok := tt.Find("错误提醒")
	if !ok || box.X < 16 || box.X+box.W > 344 || box.H < 50 {
		t.Fatalf("toast does not wrap inside the window: %+v", box)
	}
}
