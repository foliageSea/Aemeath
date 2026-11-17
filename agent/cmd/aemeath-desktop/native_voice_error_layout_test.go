package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

const nativeVoiceErrorSummary = "语音暂不可用，仍可发送文字消息"

func nativeVoiceErrorTestBounds(t *testing.T, tt *ui.Tester, label string, width, height int) ui.Rect {
	t.Helper()
	bounds, ok := tt.Find(label)
	if !ok || bounds.W <= 0 || bounds.H <= 0 || bounds.X < 226 || bounds.X+bounds.W > float32(width-19) || bounds.Y < 20 || bounds.Y+bounds.H > float32(height-19) {
		t.Fatalf("missing or out-of-window %q at %dx%d: %+v", label, width, height, bounds)
	}
	return bounds
}

func nativeVoiceErrorTestPreview(t *testing.T, tt *ui.Tester, width int, dark, expanded bool) {
	t.Helper()
	if os.Getenv("AEMEATH_VOICE_LAYOUT_PREVIEWS") == "" || (width != 760 && width != 1100) {
		return
	}
	const dir = "/tmp/aemeath-voice-layout-previews"
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("voice-error-%d-dark-%t-expanded-%t.png", width, dark, expanded))
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encodeErr := png.Encode(file, tt.Image())
	closeErr := file.Close()
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	t.Logf("voice layout preview: %s", path)
}

func TestNativeVoiceErrorNotificationLayout(t *testing.T) {
	for _, size := range [][2]int{{760, 577}, {863, 577}, {1100, 800}} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/dark=%t", size[0], size[1], dark), func(t *testing.T) {
				a, f, _, tt := nativeTest(t)
				a.service = &nativeVoiceFake{nativeFake: f}
				tt.SetSize(size[0], size[1])
				tt.SetDark(dark)
				a.voiceGlobalReady, a.voiceGlobalKey = true, "F8"
				f.state.Settings.Voice.Enabled = true
				f.state.Voice.Connected = true
				f.state.Voice.WakeReady = true
				f.state.Voice.Phase = "error"
				f.state.Voice.Error = "ASR websocket closed: unexpected EOF; " + strings.Repeat("请检查语音服务连接。", 14)
				a.refresh()
				nativeClick(t, tt, "语音助手")
				for _, expanded := range []bool{false, true} {
					if expanded {
						nativeClick(t, tt, "查看语音异常详情")
					}
					header := nativeVoiceErrorTestBounds(t, tt, "语音助手状态栏", size[0], size[1])
					banner := nativeVoiceErrorTestBounds(t, tt, "语音异常提示", size[0], size[1])
					composer := nativeVoiceErrorTestBounds(t, tt, "语音操作栏", size[0], size[1])
					if header.Y+header.H > banner.Y || banner.Y+banner.H > composer.Y+1 {
						t.Fatalf("error banner must sit between header and composer: header=%+v banner=%+v composer=%+v", header, banner, composer)
					}
					if size[0] == 1100 && !expanded && !tt.HasText("和爱弥斯聊一聊") {
						t.Fatal("wide-window empty state did not settle after first appearance")
					}
					empty := nativeVoiceErrorTestBounds(t, tt, "对话空态内容", size[0], size[1])
					if empty.Y < header.Y+header.H || empty.Y+empty.H > banner.Y {
						t.Fatalf("empty-state container overlaps header or error banner: empty=%+v header=%+v banner=%+v", empty, header, banner)
					}
					for _, text := range []string{"和爱弥斯聊一聊", fmt.Sprintf("说出「%s」，或点击手动唤醒开始对话。", a.savedWakePhrase()), "也可以在下方输入文字开始对话。"} {
						if bounds, found := tt.Find(text); found && (bounds.Y < header.Y+header.H || bounds.Y+bounds.H > banner.Y) {
							t.Fatalf("visible empty-state content overlaps header or error banner: %q bounds=%+v header=%+v banner=%+v", text, bounds, header, banner)
						}
					}
					for _, label := range []string{"文本消息", "发送文本消息", "手动唤醒", "语音设置", "清空对话", "关闭语音异常提示", "查看语音状态"} {
						nativeVoiceErrorTestBounds(t, tt, label, size[0], size[1])
					}
					top, ok := tt.AccessByRole(ui.RoleButton, "查看语音状态")
					if !ok || top.Disabled || top.Bounds.Y < header.Y || top.Bounds.Y+top.Bounds.H > header.Y+header.H {
						t.Fatalf("top error status is not an accessible header action: %+v", top)
					}
					if !tt.HasText("已连接") || !tt.HasText("语音异常") || !tt.HasText("唤醒已就绪") || !tt.HasText(nativeVoiceErrorSummary) {
						t.Fatalf("missing separate connection, error or truthful wake status: %v", tt.Texts())
					}
					if tt.HasText("唤醒未就绪") || tt.HasText("语音处理失败") || tt.HasText("语音提示："+f.state.Voice.Error) {
						t.Fatal("error state duplicated in composer or overwrote WakeReady")
					}
					rawCount := 0
					for _, text := range tt.Texts() {
						if strings.Contains(text, f.state.Voice.Error) {
							rawCount++
						}
					}
					wantRawCount := 0
					if expanded {
						wantRawCount = 1
					}
					if tt.HasText("原始信息："+f.state.Voice.Error) != expanded || rawCount != wantRawCount {
						t.Fatalf("raw diagnostic must appear only once and only in expanded details: expanded=%t rawCount=%d", expanded, rawCount)
					}
					nativeVoiceErrorTestPreview(t, tt, size[0], dark, expanded)
				}
			})
		}
	}
}

func TestNativeVoiceErrorStatusKeepsConnectionIndependent(t *testing.T) {
	for _, connected := range []bool{false, true} {
		for _, phase := range []string{"idle", "speaking", "error"} {
			t.Run(fmt.Sprintf("connected=%t/phase=%s", connected, phase), func(t *testing.T) {
				a, f, _, tt := nativeTest(t)
				a.service = &nativeVoiceFake{nativeFake: f}
				tt.SetSize(760, 577)
				f.state.Settings.Voice.Enabled = true
				f.state.Voice.Connected = connected
				f.state.Voice.WakeReady = true
				f.state.Voice.Phase = phase
				f.state.Voice.Error = "microphone permission denied"
				a.refresh()
				nativeClick(t, tt, "语音助手")
				connection := "未连接"
				if connected {
					connection = "已连接"
				}
				row, found := tt.Find("助手连接状态")
				status, ok := tt.Find(connection)
				warning, action := tt.AccessByRole(ui.RoleButton, "查看语音状态")
				if !found || !ok || !action || warning.Disabled || !tt.HasText("语音异常") {
					t.Fatalf("connection and voice failure must remain independent: connection=%q texts=%v", connection, tt.Texts())
				}
				for _, bounds := range []ui.Rect{status, warning.Bounds} {
					if bounds.X < row.X || bounds.X+bounds.W > row.X+row.W+1 || bounds.Y < row.Y || bounds.Y+bounds.H > row.Y+row.H+1 {
						t.Fatalf("separate status escaped connection row: status=%+v row=%+v", bounds, row)
					}
				}
				if status.X < warning.Bounds.X+warning.Bounds.W && status.X+status.W > warning.Bounds.X {
					t.Fatalf("connection and error status overlap: connection=%+v warning=%+v", status, warning.Bounds)
				}
				if !tt.HasText("唤醒已就绪") || a.snapshot.Voice.Phase != phase || !a.snapshot.Voice.WakeReady {
					t.Fatal("displaying an error changed the runtime phase or WakeReady")
				}
			})
		}
	}
}

func TestNativeVoiceErrorNotificationDismissReopenAndRecover(t *testing.T) {
	a, f, _, tt := nativeTest(t)
	a.service = &nativeVoiceFake{nativeFake: f}
	tt.SetSize(760, 577)
	f.state.Settings.Voice.Enabled = true
	f.state.Voice.Connected = true
	f.state.Voice.Phase = "error"
	f.state.Voice.Error = "microphone unavailable"
	a.refresh()
	nativeClick(t, tt, "语音助手")
	assertBanner := func(visible, details bool) {
		t.Helper()
		_, found := tt.Find("语音异常提示")
		if found != visible || tt.HasText("原始信息："+f.state.Voice.Error) != details {
			t.Fatalf("banner/details visibility mismatch: want=%t/%t texts=%v", visible, details, tt.Texts())
		}
	}
	assertBanner(true, false)
	nativeClick(t, tt, "查看语音异常详情")
	assertBanner(true, true)
	nativeClick(t, tt, "关闭语音异常提示")
	assertBanner(false, false)
	a.refresh()
	tt.Frame()
	assertBanner(false, false)
	if !tt.HasText("语音异常") {
		t.Fatal("dismissing a notification cleared the top error status")
	}
	nativeClick(t, tt, "查看语音状态")
	assertBanner(true, true)
	nativeClick(t, tt, "关闭语音异常提示")
	f.state.Voice.Error = "ASR service unavailable"
	a.refresh()
	tt.Frame()
	assertBanner(true, false)
	nativeClick(t, tt, "关闭语音异常提示")
	f.state.Voice.Error = ""
	f.state.Voice.Phase = "idle"
	a.refresh()
	tt.Frame()
	assertBanner(false, false)
	if tt.HasText("语音异常") {
		t.Fatal("recovered voice retains error status")
	}
	f.state.Voice.Error = "ASR service unavailable"
	f.state.Voice.Phase = "error"
	a.refresh()
	tt.Frame()
	assertBanner(true, false)
}

func TestNativeVoiceErrorKeepsTextSendingAndWakeState(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprintf("WakeReady=%t", ready), func(t *testing.T) {
			a, base, updates, tt := nativeTest(t)
			f := &nativeVoiceFake{nativeFake: base}
			a.service = f
			tt.SetSize(760, 577)
			f.state.Settings.Voice.Enabled = true
			f.state.Voice.Connected = true
			f.state.Voice.WakeReady = ready
			f.state.Voice.Phase = "error"
			f.state.Voice.Error = "voice service failed"
			a.refresh()
			nativeClick(t, tt, "语音助手")
			wake := "唤醒未就绪"
			if ready {
				wake = "唤醒已就绪"
			}
			if !tt.HasText(wake) || a.snapshot.Voice.WakeReady != ready {
				t.Fatal("voice error overwrote the actual wake readiness")
			}
			for index, action := range []string{"查看语音异常详情", "关闭语音异常提示"} {
				nativeClick(t, tt, action)
				message := fmt.Sprintf("异常时仍可发送文字%d", index)
				nativeVoiceEdit(t, tt, "文本消息", message)
				send, found := tt.AccessByRole(ui.RoleButton, "发送文本消息")
				if !found || send.Disabled {
					t.Fatalf("voice-only failure disabled text sending: %+v", send)
				}
				if index == 0 {
					tt.Key(0, ui.KeyEnter)
				} else {
					nativeClick(t, tt, "发送文本消息")
				}
				nativeComplete(t, updates, tt)
				if a.voiceText != "" {
					t.Fatal("successful text send did not clear composer")
				}
			}
			want := [][2]string{{"text", "异常时仍可发送文字0"}, {"text", "异常时仍可发送文字1"}}
			f.mu.Lock()
			got := append([][2]string(nil), f.commands...)
			f.mu.Unlock()
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("commands = %v, want %v", got, want)
			}
		})
	}
}
