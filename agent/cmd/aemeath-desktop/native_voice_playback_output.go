package main

import (
	"aemeath/agent/internal/desktop"

	"github.com/egoist/mygo/ui"
)

// Preserve legacy routes: console is the computer running Agent, while agent
// (including the empty default) is the ESP32 monitor.
func nativeVoicePlaybackIndex(route string) int {
	if route == "console" {
		return 0
	}
	return 1
}

func (a *nativeUI) switchVoicePlaybackOutput(route string) {
	if a.busy["save"] || a.snapshot.Settings.RunMode == desktop.RunModeStandalone ||
		(route != "console" && route != "agent") ||
		nativeVoicePlaybackIndex(route) == nativeVoicePlaybackIndex(a.snapshot.Settings.Voice.PlaybackOutput) {
		return
	}
	// Apply only this preference, never unrelated unsaved settings.
	settings := copySettings(a.snapshot.Settings)
	settings.Voice.PlaybackOutput = route
	draftOutput := a.draft.Voice.PlaybackOutput
	a.run("save", func() error { return a.service.SaveSettings(settings) }, func() {
		// Keep any playback edit made in settings while this request was running.
		if a.draft.Voice.PlaybackOutput == draftOutput {
			a.draft.Voice.PlaybackOutput = route
		}
		a.notify(nativeNoticeSuccess, "播放位置已切换。")
	})
}

func (a *nativeUI) voicePlaybackOutputView(c *ui.Context) {
	standalone := a.snapshot.Settings.RunMode == desktop.RunModeStandalone
	output := nativeVoicePlaybackIndex(a.snapshot.Settings.Voice.PlaybackOutput)
	if standalone {
		output = 0
	}
	ui.Row(c.Key("voice-playback-shortcut")).Label("播放位置快捷切换").Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "播放位置").FontSize(11).TextColor(c.Theme().TextMuted).Shrink(0)
		field := nativeSettingsSegmented(c, "voice-playback-quick", &output, "播放位置", "Agent", "监控台").Disabled(standalone || a.busy["save"])
		if field.Changed() {
			a.switchVoicePlaybackOutput([]string{"console", "agent"}[output])
		}
	})
}
