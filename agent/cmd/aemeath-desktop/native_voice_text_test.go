package main

import (
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"aemeath/agent/internal/brain"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNativeVoiceTextComposer(t *testing.T) {
	a, base, updates, tt := nativeTest(t)
	f := &nativeVoiceFake{nativeFake: base}
	a.service = f
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	a.refresh()
	nativeClick(t, tt, "语音助手")
	if info, ok := tt.AccessByRole(ui.RoleButton, "发送文本消息"); !ok || !info.Disabled {
		t.Fatal("empty composer must not send")
	}
	nativeVoiceEdit(t, tt, "文本消息", "  你好爱弥斯  ")
	tt.Key(0, ui.KeyEnter)
	nativeComplete(t, updates, tt)
	if a.voiceText != "" || !reflect.DeepEqual(f.commands, [][2]string{{"text", "你好爱弥斯"}}) {
		t.Fatalf("send = %v, draft = %q", f.commands, a.voiceText)
	}
	nativeVoiceEdit(t, tt, "文本消息", "第一行")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("第二行")
	if a.voiceText != "第一行\n第二行" || len(f.commands) != 1 {
		t.Fatalf("multiline draft = %q, commands = %v", a.voiceText, f.commands)
	}
	nativeClick(t, tt, "发送文本消息")
	// New edits while the queue operation completes must survive.
	nativeVoiceEdit(t, tt, "文本消息", "下一条消息")
	nativeComplete(t, updates, tt)
	if a.voiceText != "下一条消息" {
		t.Fatalf("completion erased new draft: %q", a.voiceText)
	}
	f.commandError = errors.New("设备忙")
	nativeClick(t, tt, "发送文本消息")
	nativeComplete(t, updates, tt)
	if a.voiceText != "下一条消息" || !strings.Contains(a.notice, "设备忙") {
		t.Fatalf("failure lost draft or feedback: %q, %q", a.voiceText, a.notice)
	}
	f.state.Voice.Connected = false
	a.refresh()
	tt.Frame()
	if info, ok := tt.AccessByRole(ui.RoleButton, "发送文本消息"); !ok || !info.Disabled {
		t.Fatal("offline sending must be disabled")
	}
	tt.Key(0, ui.KeyEnter)
	if len(f.commands) != 3 || a.voiceText != "下一条消息" {
		t.Fatal("offline Enter sent or erased the draft")
	}
}

func TestNativeVoiceTextLayout(t *testing.T) {
	for _, size := range [][2]int{{760, 577}, {863, 577}, {1100, 800}} {
		for _, dark := range []bool{false, true} {
			a, f, _, tt := nativeTest(t)
			a.service = &nativeVoiceFake{nativeFake: f}
			tt.SetSize(size[0], size[1])
			tt.SetDark(dark)
			f.state.Error = "device unavailable"
			a.refresh()
			nativeClick(t, tt, "语音助手")
			for _, label := range []string{"文本消息", "发送文本消息", "语音操作栏"} {
				r, ok := tt.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 || r.X < 226 || r.X+r.W > float32(size[0]-19) || r.Y < 20 || r.Y+r.H > float32(size[1]-19) {
					t.Fatalf("composer outside content at %v dark=%v: %s %+v", size, dark, label, r)
				}
			}
		}
	}
}

func TestNativeVoiceComposerAdaptiveLayout(t *testing.T) {
	for _, width := range []int{760, 1100} {
		for _, dark := range []bool{false, true} {
			a, f, _, tt := nativeTest(t)
			tt.SetSize(width, 800)
			tt.SetDark(dark)
			a.service = &nativeVoiceFake{nativeFake: f}
			f.state.Settings.Voice.Enabled = true
			f.state.Voice.Connected = true
			f.state.Voice.Phase = "idle"
			f.state.Voice.Context = brain.ContextStats{InputBudget: 28000, UsagePercent: 0.9}
			a.refresh()
			nativeClick(t, tt, "语音助手")
			nativeVoiceClickRole(t, tt, ui.RoleTextField, "文本消息")
			input, _ := tt.Find("文本消息")
			card, _ := tt.Find("语音操作栏")
			send, _ := tt.Find("发送文本消息")
			mic, _ := tt.Find("手动唤醒")
			status, _ := tt.Find("当前对话状态")
			if card.H < 140 || card.H > 160 || input.Y >= send.Y || send.X <= mic.X || send.Y != mic.Y || send.H != mic.H || status.Y < send.Y+send.H {
				t.Fatalf("unexpected composer layout width=%d: card=%+v input=%+v send=%+v mic=%+v status=%+v", width, card, input, send, mic, status)
			}
			if tt.HasText("第 2 轮") {
				t.Fatal("idle details leaked into footer")
			}
			if _, ok := tt.Find("文本输入快捷键"); ok != (width >= 900) {
				t.Fatal("shortcut hint did not adapt to width")
			}
			if dir := os.Getenv("AEMEATH_COMPOSER_PREVIEW_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				file, err := os.Create(filepath.Join(dir, fmt.Sprintf("voice-composer-%d-dark-%t.png", width, dark)))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(file, tt.Image())
				closeErr := file.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			a.voiceText = "第一行\n第二行\n第三行\n第四行"
			tt.Frame()
			expanded, _ := tt.Find("文本消息")
			if expanded.H <= input.H {
				t.Fatalf("multiline input did not grow: before=%+v after=%+v", input, expanded)
			}
			a.voiceText = strings.Repeat("很长的中文消息，用于验证自动换行和高度上限。", 80)
			tt.Frame()
			wrapped, _ := tt.Find("文本消息")
			if wrapped.H < expanded.H || wrapped.H > 120 {
				t.Fatalf("wrapped input has wrong height: %+v", wrapped)
			}
			a.voiceText = ""
			tt.Frame()
			collapsed, _ := tt.Find("文本消息")
			if collapsed.H != input.H {
				t.Fatalf("empty input did not shrink: %+v", collapsed)
			}
		}
	}
}

func TestNativeVoiceTextRejectsBlankAndOversizedDrafts(t *testing.T) {
	a, base, _, tt := nativeTest(t)
	f := &nativeVoiceFake{nativeFake: base}
	a.service = f
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	a.refresh()
	nativeClick(t, tt, "语音助手")
	for _, text := range []string{" \n\t ", strings.Repeat("爱", 1366)} {
		a.voiceText = text
		a.sendVoiceText()
		if len(f.commands) != 0 || a.voiceText != text {
			t.Fatal("invalid draft sent or erased")
		}
	}
	if !strings.Contains(a.notice, "4096") {
		t.Fatal("missing length validation feedback")
	}
}
