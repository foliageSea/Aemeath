package main

import (
	"fmt"
	"runtime"
	"strings"

	"aemeath/agent/internal/holdkey"
	"github.com/egoist/mygo/ui"
)

type nativeShortcutCapture struct {
	open  bool
	value string
	error string
}

func (a *nativeUI) beginVoiceShortcutCapture() {
	if a.busy["save"] || a.voiceShortcutClosed {
		return
	}
	a.snapshot = a.service.Snapshot()
	a.endVoiceHold(true)
	a.voiceShortcutCapture = nativeShortcutCapture{open: true}
}

func nativeCapturedShortcut(ev ui.InputEvent) (string, error) {
	return nativeCapturedShortcutForPlatform(ev, runtime.GOOS)
}

// Carbon registers US ANSI positions, whereas RegisterHotKey registers layout
// virtual keys. Keep recorded letters consistent with the respective backend.
func nativeCapturedShortcutForPlatform(ev ui.InputEvent, goos string) (string, error) {
	key := ""
	switch {
	case goos == "windows" && ev.Key >= ui.KeyA && ev.Key <= ui.KeyZ:
		key = string(rune('A' + ev.Key - ui.KeyA))
	case ev.Code >= ui.CodeKeyA && ev.Code <= ui.CodeKeyZ:
		key = string(rune('A' + ev.Code - ui.CodeKeyA))
	case ev.Code >= ui.CodeDigit1 && ev.Code <= ui.CodeDigit9:
		key = string(rune('1' + ev.Code - ui.CodeDigit1))
	case ev.Code == ui.CodeDigit0:
		key = "0"
	case ev.Code >= ui.CodeF1 && ev.Code <= ui.CodeF12:
		key = fmt.Sprintf("F%d", ev.Code-ui.CodeF1+1)
	case ev.Code >= ui.CodeF13 && ev.Code <= ui.CodeF24:
		key = fmt.Sprintf("F%d", ev.Code-ui.CodeF13+13)
	case ev.Code == ui.CodeSpace:
		key = "Space"
	case ev.Code == ui.CodeUnknown:
		k := ev.PhysicalKey
		if k == ui.KeyUnknown {
			k = ev.Key
		}
		switch {
		case k >= ui.KeyA && k <= ui.KeyZ:
			key = string(rune('A' + k - ui.KeyA))
		case k >= ui.Key0 && k <= ui.Key9:
			key = string(rune('0' + k - ui.Key0))
		case k >= ui.KeyF1 && k <= ui.KeyF12:
			key = fmt.Sprintf("F%d", k-ui.KeyF1+1)
		case k == ui.KeySpace:
			key = "Space"
		}
	}
	if key == "" {
		return "", fmt.Errorf("不支持此按键，请使用 F1–F24、字母、数字或空格。")
	}
	var parts []string
	for _, mod := range []struct {
		bit  ui.Modifiers
		name string
	}{{ui.Ctrl, "Ctrl"}, {ui.Shift, "Shift"}, {ui.Alt, "Alt"}, {ui.Super, "Super"}} {
		if ev.Mods&mod.bit != 0 {
			parts = append(parts, mod.name)
		}
	}
	value, err := holdkey.NormalizeShortcut(strings.Join(append(parts, key), "+"))
	if err != nil {
		return "", fmt.Errorf("字母、数字和空格需搭配 Ctrl、Alt / Option 或 Super / Cmd / Win。")
	}
	return value, nil
}

func (a *nativeUI) captureVoiceShortcutInput(ev ui.InputEvent) bool {
	s := &a.voiceShortcutCapture
	if !s.open {
		return false
	}
	switch ev.Kind {
	case ui.InputKeyDown:
		if ev.Repeat {
			return true
		}
		if ev.Key == ui.KeyEscape || ev.Code == ui.CodeEscape {
			s.open = false
			return true
		}
		if ev.Key == ui.KeyTab || ev.Code == ui.CodeTab {
			return false // Leave keyboard access to the confirm/cancel buttons.
		}
		switch ev.Code {
		case ui.CodeControlLeft, ui.CodeControlRight, ui.CodeShiftLeft, ui.CodeShiftRight,
			ui.CodeAltLeft, ui.CodeAltRight, ui.CodeSuperLeft, ui.CodeSuperRight:
			return true // Wait for a main key; modifiers alone are not shortcuts.
		}
		value, err := nativeCapturedShortcut(ev)
		s.value, s.error = value, ""
		if err != nil {
			s.error = err.Error()
		}
		return true
	case ui.InputKeyUp, ui.InputText, ui.InputCompose, ui.InputCommand:
		return true
	}
	return false
}

func (a *nativeUI) voiceShortcutCaptureView(c *ui.Context) {
	s := &a.voiceShortcutCapture
	ui.Modal(c.Key("voice-shortcut-capture-modal"), &s.open, func() {
		ui.Column(c.Key("voice-shortcut-capture-body")).FillWidth().MinWidth(0).Gap(12).Children(func() {
			ui.Text(c, "录入快捷键").Bold().FontSize(14)
			value := s.value
			if value == "" {
				value = "等待按键…"
			}
			ui.Box(c.Key("voice-shortcut-capture-input")).Label("快捷键录入区域").Focusable().AutoFocus().FillWidth().MinWidth(0).Padding(16).
				Background(c.Theme().Background).HandleInput(a.captureVoiceShortcutInput).Children(func() {
				ui.Text(c, value).FontSize(18).Wrap()
			})
			if s.error != "" {
				ui.Text(c, s.error).Label("快捷键录入错误").FontSize(12).TextColor(c.Theme().Warning).Wrap()
			}
			nativeSettingsActions(c, "voice-shortcut-capture-actions", func() {
				ui.PrimaryButton(c.Key("voice-shortcut-capture-confirm"), "使用快捷键").Disabled(s.value == "" || s.error != "").OnClick(func() {
					if a.draft.VoiceShortcut != s.value {
						a.draft.VoiceShortcut = s.value
						a.edited()
					}
					s.open = false
				})
				ui.Button(c.Key("voice-shortcut-capture-cancel"), "取消录入").OnClick(func() { s.open = false })
			})
		})
	}).Label("录入快捷键").Width(480)
}
