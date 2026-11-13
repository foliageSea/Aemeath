package main

import (
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aemeath/agent/internal/voice"
	"github.com/egoist/mygo/ui"
)

func TestNativeVoiceReplayUsesSourceIdentityAndStopsWithoutAddingHistory(t *testing.T) {
	a, base, updates, tt := nativeTest(t)
	f := &nativeVoiceFake{nativeFake: base}
	a.service = f
	f.state.Settings.Voice.Enabled = true
	f.state.Voice = voice.State{Connected: true, Session: 42, Turn: 7, Phase: "idle", Transcript: "问题", Response: "缓存的回复", ReplayTurns: []uint32{7}}
	a.refresh()
	nativeClick(t, tt, "语音助手")
	a.voiceText = "保留草稿"
	tt.Frame()
	button, ok := tt.AccessByRole(ui.RoleButton, "重播")
	if !ok || button.Disabled {
		t.Fatalf("missing enabled replay: %+v", button)
	}
	response, ok := tt.Find("缓存的回复")
	if !ok || button.Bounds.Y < response.Y+response.H {
		t.Fatal("replay is not below the assistant reply")
	}
	nativeClick(t, tt, "重播")
	nativeComplete(t, updates, tt)
	if !reflect.DeepEqual(f.commands, [][2]string{{"replay", "42:7"}}) {
		t.Fatalf("commands = %v", f.commands)
	}
	original := a.voiceHistory[0]
	// Replay opens a new logical turn with no transcript. Its source, not its
	// logical turn number, controls the button on the archived message.
	f.state.Voice = voice.State{Connected: true, Session: 42, Turn: 8, Phase: "speaking", Response: "缓存的回复", ReplayingTurn: 7}
	a.refresh()
	tt.Frame()
	if len(a.voiceHistory) != 1 || !reflect.DeepEqual(a.voiceHistory[0], original) {
		t.Fatalf("replay changed transcript history: %+v", a.voiceHistory)
	}
	button, ok = tt.AccessByRole(ui.RoleButton, "停止")
	if !ok || button.Disabled {
		t.Fatalf("missing enabled stop with no advertised cache: %+v", button)
	}
	nativeClick(t, tt, "停止")
	nativeComplete(t, updates, tt)
	if !reflect.DeepEqual(f.commands, [][2]string{{"replay", "42:7"}, {"cancel", ""}}) {
		t.Fatalf("commands = %v", f.commands)
	}
	if a.voiceText != "保留草稿" {
		t.Fatalf("replay changed draft: %q", a.voiceText)
	}
}

func TestNativeVoiceRespeakExplicitForRestoredAndOldSessions(t *testing.T) {
	for _, restored := range []bool{false, true} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("restored=%v/cached=%v", restored, cached), func(t *testing.T) {
				a, base, updates, _ := nativeTest(t)
				f := &nativeVoiceFake{nativeFake: base}
				a.service = f
				f.state.Settings.Voice.Enabled = true
				f.state.Voice = voice.State{Connected: true, Session: 42, Turn: 9, Phase: "thinking", ReplayTurns: []uint32{7}}
				if cached {
					f.state.Voice.ReplayKeys = []string{voice.ReplayTextKey("旧回复")}
				}
				a.refresh()
				turn := nativeVoiceTurn{turn: 7, restored: restored, response: " 旧回复 ", state: voice.State{Session: 41}}
				if restored {
					turn.turn, turn.state.Session = 0, 0
				}
				a.voiceHistory = []nativeVoiceTurn{turn}
				a.voiceText = "草稿"
				tt := ui.NewTester(func(c *ui.Context) { a.voiceReplayView(c, turn) }, 800, 160)
				label := "合成播放"
				if cached {
					label = "重播"
					nativeVoiceAssertTooltip(t, tt, label, "重播本条回复的缓存语音，不会重新生成语音。")
					if tt.HasText("可能收费") {
						t.Fatal("cached reply showed synthesis cost warning")
					}
				} else {
					nativeVoiceAssertTooltip(t, tt, label, voiceReplayCostHint)
					if !tt.HasText("可能收费") {
						t.Fatal("missing visible cost warning")
					}
				}
				button, ok := tt.AccessByRole(ui.RoleButton, label)
				if !ok || button.Disabled {
					t.Fatalf("missing enabled action: %+v", button)
				}
				tt.Frame()
				if len(f.commands) != 0 || a.busy["voice-command"] {
					t.Fatal("render synthesized without an explicit click")
				}
				nativeClick(t, tt, label)
				nativeComplete(t, updates, tt)
				if !reflect.DeepEqual(f.commands, [][2]string{{"respeak", "旧回复"}}) {
					t.Fatalf("commands = %v", f.commands)
				}
				if !reflect.DeepEqual(a.voiceHistory, []nativeVoiceTurn{turn}) || a.voiceText != "草稿" {
					t.Fatal("respeak changed history or draft")
				}
			})
		}
	}
}

func TestNativeVoiceReplayDisabledGuards(t *testing.T) {
	for _, test := range []struct {
		name                                      string
		text                                      string
		disconnected, disabled, busy, unsupported bool
	}{
		{name: "disconnected", text: "回复", disconnected: true},
		{name: "disabled", text: "回复", disabled: true},
		{name: "busy", text: "回复", busy: true},
		{name: "unsupported", text: "回复", unsupported: true},
		{name: "empty"}, {name: "whitespace", text: " \n "},
		{name: "invalid-utf8", text: string([]byte{0xff})},
		{name: "too-many-bytes", text: strings.Repeat("中", voice.MaxReplayTextBytes/3+1)},
	} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cached=%v", test.name, cached), func(t *testing.T) {
				a, base, _, _ := nativeTest(t)
				f := &nativeVoiceFake{nativeFake: base}
				a.service = f
				a.snapshot.Settings.Voice.Enabled = !test.disabled
				a.snapshot.Voice = voice.State{Connected: !test.disconnected, Session: 42, Turn: 9, Phase: "idle"}
				if cached {
					a.snapshot.Voice.ReplayTurns = []uint32{7}
				}
				if test.busy {
					a.busy["voice-command"] = true
				}
				if test.unsupported {
					a.service = base
				}
				turn := nativeVoiceTurn{turn: 7, response: test.text, state: voice.State{Session: 42}}
				tt := ui.NewTester(func(c *ui.Context) { a.voiceReplayView(c, turn) }, 800, 160)
				label := "合成播放"
				if cached {
					label = "重播"
				}
				button, ok := tt.AccessByRole(ui.RoleButton, label)
				if !ok || !button.Disabled {
					t.Fatalf("invalid replay enabled: %+v", button)
				}
				nativeClick(t, tt, label)
				a.replayVoiceTurn(turn) // A stale handler must recheck the guards.
				if len(f.commands) != 0 || a.busy["voice-command"] != test.busy {
					t.Fatalf("disabled replay issued command: %v", f.commands)
				}
			})
		}
	}
}

func TestNativeVoiceRespeakIncompleteCurrentReply(t *testing.T) {
	for _, phase := range []string{"thinking", "recognizing", "synthesizing", "speaking"} {
		for _, cache := range []string{"none", "turn", "key"} {
			t.Run(phase+"/"+cache, func(t *testing.T) {
				a, base, updates, _ := nativeTest(t)
				f := &nativeVoiceFake{nativeFake: base}
				a.service = f
				f.state.Settings.Voice.Enabled = true
				f.state.Voice = voice.State{Connected: true, Session: 42, Turn: 7, Phase: phase}
				if cache == "turn" {
					f.state.Voice.ReplayTurns = []uint32{7}
				}
				if cache == "key" {
					f.state.Voice.ReplayKeys = []string{voice.ReplayTextKey("生成中的回复")}
				}
				a.refresh()
				turn := nativeVoiceTurn{turn: 7, response: "生成中的回复", state: voice.State{Session: 42}}
				tt := ui.NewTester(func(c *ui.Context) { a.voiceReplayView(c, turn) }, 800, 160)
				label := "重播"
				if cache == "none" {
					label = "合成播放"
				}
				button, ok := tt.AccessByRole(ui.RoleButton, label)
				if cache != "none" && (!ok || button.Disabled) {
					t.Fatalf("button = %+v", button)
				}
				if cache == "none" {
					if ok {
						t.Fatal("incomplete reply displayed an action")
					}
					if !tt.HasText("语音生成中…") {
						t.Fatal("missing incomplete reply hint")
					}
					a.replayVoiceTurn(turn)
					if len(f.commands) != 0 || a.busy["voice-command"] {
						t.Fatal("incomplete reply synthesized")
					}
				} else {
					nativeClick(t, tt, label)
					nativeComplete(t, updates, tt)
					want := [][2]string{{"respeak", "生成中的回复"}}
					if cache == "turn" {
						want = [][2]string{{"replay", "42:7"}}
					}
					if !reflect.DeepEqual(f.commands, want) {
						t.Fatalf("commands = %v", f.commands)
					}
				}
			})
		}
	}
}

func TestNativeVoiceReplayUsesLiveCacheAndOnlyStopsMatchingSource(t *testing.T) {
	a, base, _, _ := nativeTest(t)
	a.service = &nativeVoiceFake{nativeFake: base}
	a.snapshot.Settings.Voice.Enabled = true
	a.snapshot.Voice = voice.State{Connected: true, Session: 42, Turn: 10, ReplayTurns: []uint32{3}, ReplayingTurn: 3}
	first := nativeVoiceTurn{turn: 3, response: "首条回复", state: voice.State{Session: 42}}
	second := nativeVoiceTurn{turn: 7, response: "另一条回复", state: voice.State{Session: 42, ReplayTurns: []uint32{7}}}
	for _, test := range []struct {
		turn     nativeVoiceTurn
		label    string
		disabled bool
	}{{first, "停止", false}, {second, "合成播放", false}} {
		tt := ui.NewTester(func(c *ui.Context) { a.voiceReplayView(c, test.turn) }, 800, 160)
		button, ok := tt.AccessByRole(ui.RoleButton, test.label)
		if !ok || button.Disabled != test.disabled {
			t.Fatalf("source %d: %+v", test.turn.turn, button)
		}
	}
	a.snapshot.Voice.ReplayingTurn = 0
	a.snapshot.Voice.ReplayTurns = nil
	available, replaying := a.voiceReplayState(first)
	if available || replaying {
		t.Fatal("archived replay state survived cache eviction")
	}
	a.snapshot.Voice.ReplayTurns = []uint32{7}
	available, replaying = a.voiceReplayState(second)
	if !available || replaying {
		t.Fatal("new live cache did not enable archived reply")
	}
}

func TestNativeVoiceReplayFailureDoesNotSynthesize(t *testing.T) {
	a, base, updates, _ := nativeTest(t)
	f := &nativeVoiceFake{nativeFake: base, commandError: errors.New("语音缓存已过期")}
	a.service = f
	f.state.Settings.Voice.Enabled = true
	f.state.Voice = voice.State{Connected: true, Session: 42, Turn: 8, ReplayTurns: []uint32{7}}
	a.refresh()
	turn := nativeVoiceTurn{turn: 7, response: "缓存的回复", state: voice.State{Session: 42}}
	tt := ui.NewTester(func(c *ui.Context) { a.voiceReplayView(c, turn) }, 800, 160)
	nativeClick(t, tt, "重播")
	nativeComplete(t, updates, tt)
	if !reflect.DeepEqual(f.commands, [][2]string{{"replay", "42:7"}}) || !strings.Contains(a.notice, "语音缓存已过期") {
		t.Fatalf("commands=%v notice=%q", f.commands, a.notice)
	}
}

func TestNativeVoiceRespeakStopsByTextKeyDuringSynthesisAndPlayback(t *testing.T) {
	for _, phase := range []string{"synthesizing", "speaking"} {
		for _, restored := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restored=%v", phase, restored), func(t *testing.T) {
				a, base, updates, _ := nativeTest(t)
				f := &nativeVoiceFake{nativeFake: base}
				a.service = f
				f.state.Settings.Voice.Enabled = true
				f.state.Voice = voice.State{Connected: true, Session: 42, Turn: 10, Phase: phase, ReplayingKey: voice.ReplayTextKey("源回复")}
				a.refresh()
				turn := nativeVoiceTurn{turn: 7, restored: restored, response: " 源回复 ", state: voice.State{Session: 41}}
				tt := ui.NewTester(func(c *ui.Context) { a.voiceReplayView(c, turn) }, 800, 160)
				button, ok := tt.AccessByRole(ui.RoleButton, "停止")
				if !ok || button.Disabled {
					t.Fatalf("missing stop during %s: %+v", phase, button)
				}
				other := turn
				other.response = "另一条回复"
				if _, replaying := a.voiceReplayState(other); replaying {
					t.Fatal("unrelated reply matched active replay key")
				}
				nativeClick(t, tt, "停止")
				nativeComplete(t, updates, tt)
				if !reflect.DeepEqual(f.commands, [][2]string{{"cancel", ""}}) {
					t.Fatalf("commands = %v", f.commands)
				}
			})
		}
	}
}

func TestNativeVoiceReplayCompactLayout(t *testing.T) {
	for _, width := range []int{200, 640} {
		for _, dark := range []bool{false, true} {
			a, base, _, _ := nativeTest(t)
			a.service = &nativeVoiceFake{nativeFake: base}
			a.snapshot.Settings.Voice.Enabled = true
			a.snapshot.Voice = voice.State{Connected: true, Session: 42, Phase: "idle"}
			turn := nativeVoiceTurn{response: "旧回复", restored: true}
			tt := ui.NewTester(func(c *ui.Context) { a.voiceReplayView(c, turn) }, width, 160)
			tt.SetDark(dark)
			tt.Frame()
			action, ok := tt.AccessByRole(ui.RoleButton, "合成播放")
			if !ok || action.Bounds.H != 28 || action.Bounds.X < 48 {
				t.Fatalf("bad compact action: %+v", action)
			}
			info, ok := tt.AccessByRole(ui.RoleButton, "可能收费")
			if !ok || info.Bounds.X+info.Bounds.W > float32(width) || info.Bounds.Y+info.Bounds.H > 160 {
				t.Fatalf("cost info overflow at %d: %+v", width, info)
			}
			if width == 200 && info.Bounds.Y <= action.Bounds.Y {
				t.Fatal("narrow actions did not wrap")
			}
			nativeClick(t, tt, "可能收费")
			if a.notice != voiceReplayCostHint {
				t.Fatal("cost details inaccessible by click")
			}
		}
	}
}

func TestNativeVoiceReplayMessageLayoutPreview(t *testing.T) {
	a, base, _, _ := nativeTest(t)
	a.service = &nativeVoiceFake{nativeFake: base}
	a.snapshot.Settings.Voice.Enabled = true
	a.snapshot.Voice = voice.State{Connected: true, Session: 42, Phase: "idle", ReplayTurns: []uint32{1}}
	turns := []nativeVoiceTurn{
		{turn: 1, response: "是呀，周末来啦。今晚好好歇一歇吧。", state: voice.State{Session: 42}},
		{response: "东莞今天气温约 21°C 到 27°C，有小雨。早晚稍凉，出门带把伞。", restored: true},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).FillWidth().Padding(20).Gap(24).Children(func() {
			for i, turn := range turns {
				ui.Column(c.Key(fmt.Sprint(i))).FillWidth().MinWidth(0).Gap(6).Children(func() {
					a.voiceBubble(c, "爱弥斯", turn.response, false, "")
					a.voiceReplayView(c, turn)
				})
			}
		})
	}, 640, 360)
	tt.SetDark(true)
	tt.Frame()
	body, _ := tt.Find(turns[0].response)
	action, _ := tt.AccessByRole(ui.RoleButton, "重播")
	// Body has 14px bottom padding inside its bubble, then the 6px group gap.
	if delta := action.Bounds.Y - (body.Y + body.H); delta < 19 || delta > 22 {
		t.Fatalf("message action detached: gap=%v", delta)
	}
	if dir := os.Getenv("AEMEATH_REPLAY_PREVIEW_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, "voice-replay-layout.png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image())
		closeErr := f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

func TestNativeVoiceReplayStatusAndRetry(t *testing.T) {
	for _, phase := range []string{"synthesizing", "speaking", "error"} {
		a, base, updates, _ := nativeTest(t)
		f := &nativeVoiceFake{nativeFake: base}
		a.service = f
		f.state.Settings.Voice.Enabled = true
		f.state.Voice = voice.State{Connected: true, Session: 42, Phase: phase, Response: "源回复", ReplayingKey: voice.ReplayTextKey("源回复")}
		if phase == "error" {
			f.state.Voice.ReplayingKey = ""
			f.state.Voice.ErrorStage = "synthesizing"
			f.state.Voice.Error = "合成服务暂不可用"
		}
		a.refresh()
		turn := nativeVoiceTurn{response: "源回复", restored: true}
		tt := ui.NewTester(func(c *ui.Context) { a.voiceReplayView(c, turn) }, 400, 100)
		status, label := "正在合成…", "停止"
		if phase == "speaking" {
			status = "正在播放"
		}
		if phase == "error" {
			status, label = "合成失败", "重试"
		}
		if !tt.HasText(status) {
			t.Fatal("missing status", status)
		}
		nativeClick(t, tt, label)
		nativeComplete(t, updates, tt)
		want := [][2]string{{"cancel", ""}}
		if phase == "error" {
			want = [][2]string{{"respeak", "源回复"}}
		}
		if !reflect.DeepEqual(f.commands, want) {
			t.Fatal(f.commands)
		}
	}
}

func TestNativeVoiceRespeakAllowsExactByteLimit(t *testing.T) {
	a, base, updates, _ := nativeTest(t)
	f := &nativeVoiceFake{nativeFake: base}
	a.service = f
	f.state.Settings.Voice.Enabled = true
	f.state.Voice = voice.State{Connected: true, Session: 42, Turn: 9, Phase: "idle"}
	a.refresh()
	text := strings.Repeat("中", voice.MaxReplayTextBytes/3) + "a"
	if len(text) != voice.MaxReplayTextBytes {
		t.Fatalf("test text bytes = %d", len(text))
	}
	turn := nativeVoiceTurn{turn: 7, response: " " + text + " ", state: voice.State{Session: 42}}
	tt := ui.NewTester(func(c *ui.Context) { a.voiceReplayView(c, turn) }, 800, 160)
	button, ok := tt.AccessByRole(ui.RoleButton, "合成播放")
	if !ok || button.Disabled {
		t.Fatalf("exact limit disabled: %+v", button)
	}
	nativeClick(t, tt, "合成播放")
	nativeComplete(t, updates, tt)
	if !reflect.DeepEqual(f.commands, [][2]string{{"respeak", text}}) {
		t.Fatalf("commands count = %d", len(f.commands))
	}
}
