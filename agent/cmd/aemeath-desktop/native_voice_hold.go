package main

import "github.com/egoist/mygo/ui"

// VoiceCommand only queues actions; keep press/release on the UI thread so a
// quick tap cannot reorder them or drop release behind the async busy flag.
func (a *nativeUI) startVoiceHold() bool {
	if a.voiceHold || a.voiceHoldRepeat || a.voiceDisabled() {
		return false
	}
	service := a.service.(nativeVoiceService)
	if err := service.VoiceCommand("record_start", ""); err != nil {
		a.notify(err.Error())
		return false
	}
	a.voiceHold = true
	a.voiceHoldSession = a.snapshot.Voice.Session
	a.voiceHoldStartTurn = a.snapshot.Voice.Turn
	return true
}

func (a *nativeUI) endVoiceHold(discard bool) {
	if !a.voiceHold {
		return
	}
	a.voiceHold = false
	// Never send a release/cancel into a replacement connection.
	if a.snapshot.Voice.Session != a.voiceHoldSession || !a.snapshot.Voice.Connected {
		return
	}
	kind := "record_stop"
	if discard {
		kind = "record_cancel"
	}
	if service, ok := a.service.(nativeVoiceService); ok {
		if err := service.VoiceCommand(kind, ""); err != nil {
			a.notify(err.Error())
		}
	}
}

func (a *nativeUI) voiceHoldInput(ev ui.InputEvent) {
	if ev.Key == ui.KeyF8 && (ev.Kind == ui.InputKeyDown || ev.Kind == ui.InputKeyUp) {
		a.voiceHoldRepeat = ev.Kind == ui.InputKeyDown && ev.Repeat
	}
	if !a.voiceHold {
		return
	}
	if ev.Kind == ui.InputKeyUp && ev.Key == ui.KeyF8 {
		a.endVoiceHold(false)
	}
	if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyEscape {
		a.endVoiceHold(true)
	}
}
