package main

import (
	"fmt"
	"strings"
	"sync"

	"aemeath/agent/internal/holdkey"
	"github.com/egoist/mygo/ui"
)

// All registration, activation and cleanup run on the UI/main thread.
func (a *nativeUI) installConfiguredVoiceHold(register func(string, func(bool)) (func(), error)) func() {
	a.voiceShortcutRegister = register
	commit, _, err := a.prepareVoiceShortcut(a.snapshot.Settings.VoiceShortcut)
	if err != nil {
		a.voiceGlobalError = "全局快捷键不可用：" + err.Error()
		a.notify(nativeNoticeError, a.voiceGlobalError)
	} else {
		commit()
	}
	return func() {
		if a.voiceShortcutClosed {
			return
		}
		a.voiceShortcutClosed = true
		a.voiceGlobalReady = false
		a.voiceShortcutGeneration++
		if a.voiceShortcutPending != nil {
			a.voiceShortcutPending()
			a.voiceShortcutPending = nil
		}
		if a.voiceShortcutStop != nil {
			a.voiceShortcutStop()
			a.voiceShortcutStop = nil
		}
		a.snapshot = a.service.Snapshot()
		a.endVoiceHold(true)
		a.voiceKeyDown = false
	}
}

// Reserve the replacement first, keeping the working shortcut until persistence
// succeeds. Generations reject delayed events from old or abandoned listeners.
func (a *nativeUI) prepareVoiceShortcut(requested string) (commit, cancel func(), err error) {
	noop := func() {}
	if a.voiceShortcutRegister == nil {
		return noop, noop, nil
	}
	if a.voiceShortcutClosed {
		return nil, nil, fmt.Errorf("窗口已关闭")
	}
	candidates := []string{requested}
	if requested == "" {
		candidates = []string{"F8", "Ctrl+Shift+F8"}
	}
	for _, key := range candidates {
		if a.voiceGlobalReady && key == a.voiceGlobalKey {
			return func() { a.voiceGlobalError = "" }, noop, nil
		}
		a.voiceShortcutNext++
		generation := a.voiceShortcutNext
		stop, registerErr := a.voiceShortcutRegister(key, func(down bool) {
			a.update(func() {
				if !a.voiceShortcutClosed && a.voiceGlobalReady && a.voiceShortcutGeneration == generation {
					a.globalVoiceKey(down)
				}
			})
		})
		if registerErr != nil {
			err = fmt.Errorf("%s：%w", key, registerErr)
			continue
		}
		var once sync.Once
		cancel = func() { once.Do(stop) }
		commit = func() {
			if a.voiceShortcutClosed {
				cancel()
				return
			}
			a.voiceGlobalReady = false
			a.snapshot = a.service.Snapshot()
			a.endVoiceHold(true)
			a.voiceKeyDown = false
			if a.voiceShortcutStop != nil {
				a.voiceShortcutStop()
			}
			a.voiceShortcutStop = cancel
			a.voiceShortcutGeneration = generation
			a.voiceGlobalKey, a.voiceGlobalError, a.voiceGlobalReady = key, "", true
		}
		return commit, cancel, nil
	}
	return nil, nil, err
}

func (a *nativeUI) saveWithVoiceShortcut() {
	if a.busy["save"] {
		return
	}
	dispatch := a.update
	if dispatch == nil {
		a.notify(nativeNoticeWarning, "窗口尚未就绪，请稍后重试。")
		return
	}
	settings, revision := copySettings(a.draft), a.revision
	settings.VoiceShortcut = strings.TrimSpace(settings.VoiceShortcut)
	if settings.VoiceShortcut != "" {
		key, err := holdkey.NormalizeShortcut(settings.VoiceShortcut)
		if err != nil {
			a.notify(nativeNoticeError, "语音触发快捷键无效："+err.Error())
			return
		}
		settings.VoiceShortcut = key
	}
	commit, cancel, err := a.prepareVoiceShortcut(settings.VoiceShortcut)
	if err != nil {
		a.notify(nativeNoticeWarning, "快捷键未更改，原快捷键仍保留："+err.Error())
		return
	}
	a.voiceShortcutPending = cancel
	a.busy["save"] = true
	go func() {
		err := a.service.SaveSettings(settings)
		dispatch(func() {
			a.busy["save"] = false
			a.voiceShortcutPending = nil
			if err != nil {
				cancel()
				a.notify(nativeNoticeError, "操作失败，修改已保留："+err.Error())
			} else {
				commit()
				if a.revision == revision {
					a.dirty = false
					a.draft.VoiceShortcut = settings.VoiceShortcut
				}
				a.quotaUnsaved = false
				a.notify(nativeNoticeSuccess, "设置已保存。")
			}
			a.refresh()
		})
	}()
}

func (a *nativeUI) voiceShortcutSettingsView(c *ui.Context) {
	nativeSettingsSection(c, "voice-shortcut-card", "触发快捷键", func() {
		nativeSettingsField(c, "voice-shortcut-field", "按住说话快捷键", func() ui.Element {
			field := ui.TextInput(c.Key("voice-shortcut"), &a.draft.VoiceShortcut).Label("按住说话快捷键").Placeholder("自动（F8 / Ctrl+Shift+F8）").FillWidth().MinWidth(0)
			if field.Changed() {
				a.edited()
			}
			return field
		})
		if value := strings.TrimSpace(a.draft.VoiceShortcut); value != "" {
			if _, err := holdkey.NormalizeShortcut(value); err != nil {
				ui.Text(c, "格式无效："+err.Error()).Label("快捷键格式错误").FontSize(12).Wrap()
			}
		}
		status := "当前快捷键未就绪"
		if a.voiceGlobalReady {
			status = "当前生效：" + a.voiceGlobalKey
		}
		if a.voiceGlobalError != "" {
			status = a.voiceGlobalError
		}
		ui.Text(c, status).Label("当前触发快捷键").FontSize(12).Wrap()
		ui.Button(c.Key("voice-shortcut-default"), "恢复默认快捷键").OnClick(func() { a.draft.VoiceShortcut = ""; a.edited() })
	})
}
