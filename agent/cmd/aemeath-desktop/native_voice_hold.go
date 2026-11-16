package main

func (a *nativeUI) globalVoiceKey(down bool) {
	if down == a.voiceKeyDown {
		return
	}
	a.voiceKeyDown = down
	if a.voiceShortcutCapture.open {
		// Registered global keys may be consumed by the OS before the window
		// receives them. Record that binding here without starting voice input.
		if down {
			a.voiceShortcutCapture.value = a.voiceGlobalKey
			a.voiceShortcutCapture.error = ""
		}
		return
	}
	// Refresh before either edge: background windows may not have rendered a
	// new snapshot, and a release must never target a replacement session.
	a.snapshot = a.service.Snapshot()
	if down {
		a.startVoiceHold()
	} else {
		a.endVoiceHold(false)
	}
}

// VoiceCommand only queues actions; preserve the native press/release order
// without the asynchronous button-action busy flag swallowing a quick release.
func (a *nativeUI) startVoiceHold() bool {
	if a.voiceHold || a.voiceDisabled() {
		return false
	}
	service := a.service.(nativeVoiceService)
	if err := service.VoiceCommand("record_start", ""); err != nil {
		a.notify(nativeNoticeError, err.Error())
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
	if a.snapshot.Voice.Session != a.voiceHoldSession || !a.snapshot.Voice.Connected {
		return
	}
	kind := "record_stop"
	if discard {
		kind = "record_cancel"
	}
	if service, ok := a.service.(nativeVoiceService); ok {
		if err := service.VoiceCommand(kind, ""); err != nil {
			a.notify(nativeNoticeError, err.Error())
		}
	}
}
