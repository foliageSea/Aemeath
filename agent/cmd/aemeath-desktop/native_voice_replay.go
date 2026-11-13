package main

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"aemeath/agent/internal/voice"
	"github.com/egoist/mygo/ui"
)

// Turn identities belong to the current connection. Text identities can also
// reuse complete audio for restored messages and replies from older sessions.
func (a *nativeUI) voiceReplayTurnCached(turn nativeVoiceTurn) bool {
	state := a.snapshot.Voice
	return !turn.restored && turn.turn != 0 && turn.state.Session == state.Session && slices.Contains(state.ReplayTurns, turn.turn)
}

func (a *nativeUI) voiceReplayState(turn nativeVoiceTurn) (available, replaying bool) {
	state := a.snapshot.Voice
	key := voice.ReplayTextKey(turn.response)
	textPresent := strings.TrimSpace(turn.response) != ""
	available = a.voiceReplayTurnCached(turn) || (textPresent && slices.Contains(state.ReplayKeys, key))
	replaying = (textPresent && state.ReplayingKey == key) ||
		(!turn.restored && turn.turn != 0 && turn.state.Session == state.Session && state.ReplayingTurn == turn.turn)
	return
}

func (a *nativeUI) voiceReplayIncomplete(turn nativeVoiceTurn) bool {
	if !sameVoiceTurn(turn, a.snapshot.Voice) {
		return false
	}
	switch a.snapshot.Voice.Phase {
	case "thinking", "recognizing", "synthesizing", "speaking":
		return true
	}
	return false
}

func (a *nativeUI) voiceReplayDisabled(turn nativeVoiceTurn, available, replaying bool) bool {
	if a.voiceDisabled() {
		return true
	}
	// Cancel remains possible throughout synthesis and playback.
	if replaying {
		return false
	}
	text := strings.TrimSpace(turn.response)
	return text == "" || !utf8.ValidString(text) || len(text) > voice.MaxReplayTextBytes || (!available && a.voiceReplayIncomplete(turn))
}

func (a *nativeUI) replayVoiceTurn(turn nativeVoiceTurn) {
	available, replaying := a.voiceReplayState(turn)
	if a.voiceReplayDisabled(turn, available, replaying) {
		return
	}
	if replaying {
		a.voiceCommand("cancel", "")
	} else if a.voiceReplayTurnCached(turn) {
		a.voiceCommand("replay", fmt.Sprintf("%d:%d", turn.state.Session, turn.turn))
	} else {
		// Only an explicit click requests synthesis. A failed replay never falls
		// back here; respeak itself first checks the backend text-key cache.
		a.voiceCommand("respeak", strings.TrimSpace(turn.response))
	}
}

const voiceReplayCostHint = "未保留音频，将按当前音色重新合成，可能产生 TTS 费用；不会重新询问模型。"

var (
	voiceReplaySpeakerIcon = sidebarSVG("<path d='M11 5 6 9H3v6h3l5 4V5Z'/><path d='M15 8a6 6 0 0 1 0 8M18 5a10 10 0 0 1 0 14'/>")
	voiceReplayStopIcon    = sidebarSVG("<rect x='6' y='6' width='12' height='12' rx='1'/>")
	voiceReplayInfoIcon    = sidebarSVG("<circle cx='12' cy='12' r='9'/><path d='M12 11v6M12 7v1'/>")
)

// Keep the action's hit area comfortable while leaving message text dominant.
func voiceReplayAction(c *ui.Context, key, label, hint string, icon *ui.SVG, disabled bool, action func()) {
	t := c.Theme()
	color := t.TextMuted
	if disabled {
		color = color.Alpha(0.5)
	}
	button := ui.ButtonBase(c.Key(key)).Label(label).Tooltip(hint).Height(28).Padding(0, 6).Gap(5).Shrink(0).Radius(6).AlignItems(ui.Center).Disabled(disabled)
	if !disabled && button.Hovered() {
		button.Background(t.SurfaceHover)
	}
	button.Children(func() {
		ui.Icon(c, icon).Size(14, 14).TextColor(color)
		ui.Text(c, label).FontSize(12).TextColor(color)
	}).OnClick(action)
}

func (a *nativeUI) voiceReplayView(c *ui.Context, turn nativeVoiceTurn) {
	available, replaying := a.voiceReplayState(turn)
	state := a.snapshot.Voice
	label, hint, status := "重播", "重播本条回复的缓存语音，不会重新生成语音。", ""
	disabled := a.voiceReplayDisabled(turn, available, replaying)
	showCost := !available && !replaying
	failed := state.Phase == "error" && state.ErrorStage == "synthesizing" && strings.TrimSpace(state.Response) == strings.TrimSpace(turn.response)
	icon := voiceReplaySpeakerIcon
	if replaying {
		label, hint, icon = "停止", "停止合成或重播本条回复。", voiceReplayStopIcon
		status = "正在播放"
		if state.Phase == "synthesizing" || state.Phase == "thinking" {
			status = "正在合成…"
		}
	} else if !available {
		label, hint = "合成播放", voiceReplayCostHint
		if a.voiceReplayIncomplete(turn) {
			status, showCost = "语音生成中…", false
		}
	}
	if failed && !replaying {
		label, status = "重试", "合成失败"
		hint = state.Error
	}
	text := strings.TrimSpace(turn.response)
	if !replaying && (text == "" || !utf8.ValidString(text)) {
		hint, status, showCost = "没有可朗读的有效回复文本。", "无有效回复文本", false
	} else if !replaying && len(text) > voice.MaxReplayTextBytes {
		hint, status, showCost = fmt.Sprintf("回复文本超过 %d 字节，无法重播或重新合成语音。", voice.MaxReplayTextBytes), "回复过长，无法合成", false
	}
	if !state.Connected {
		hint = "连接语音助手后可播放。"
	}
	ui.Row(c.Key("voice-replay")).Label("回复语音操作").FillWidth().MinWidth(0).Padding(0, 0, 0, 48).Wrap().GapX(8).GapY(4).AlignItems(ui.Center).Children(func() {
		if status != "" {
			ui.Text(c.Key("status"), status).FontSize(11).TextColor(c.Theme().TextMuted).Tooltip(hint).MinWidth(0).Wrap()
		}
		// During the original reply's generation only show its muted status.
		if !(a.voiceReplayIncomplete(turn) && !available && !replaying) {
			voiceReplayAction(c, "action", label, hint, icon, disabled, func() { a.replayVoiceTurn(turn) })
		}
		if showCost {
			voiceReplayAction(c, "cost-info", "可能收费", voiceReplayCostHint, voiceReplayInfoIcon, false, func() { a.notify(nativeNoticeInfo, voiceReplayCostHint) })
		}
	})
}
