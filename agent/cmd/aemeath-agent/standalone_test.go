package main

import (
	"context"
	"strings"
	"testing"

	"aemeath/agent/internal/voice"
)

func TestCLIStandaloneHostReportsDeviceUnavailable(t *testing.T) {
	host := &cliVoiceHost{}
	for _, page := range []string{"metrics", "quota", "voice"} {
		if err := host.SetPage(context.Background(), page); err == nil || !strings.Contains(err.Error(), "device unavailable") {
			t.Fatalf("SetPage(%q)=%v", page, err)
		}
	}
	if err := host.SetScreen(context.Background(), true, 70); err == nil || !strings.Contains(err.Error(), "device unavailable") {
		t.Fatalf("SetScreen=%v", err)
	}
	host.setMetrics(map[string]int{"cpu": 25})
	host.setQuota(map[string]int{"remaining": 10})
	if host.Metrics() != `{"cpu":25}` || host.Quota() != `{"remaining":10}` {
		t.Fatalf("standalone local snapshots: metrics=%s quota=%s", host.Metrics(), host.Quota())
	}
}

func TestCLIConsoleManualCommands(t *testing.T) {
	commands := make(chan voice.Command, 8)
	control := &cliVoiceControl{commands: commands, connected: true}
	control.console(context.Background(), strings.NewReader("record_start\nrecord_stop\nrecord_cancel\ntext 你好，爱弥斯。\ncancel\n"))
	want := []voice.Command{{Kind: "record_start"}, {Kind: "record_stop"}, {Kind: "record_cancel"}, {Kind: "text", Text: "你好，爱弥斯。"}, {Kind: "cancel"}}
	if len(commands) != len(want) {
		t.Fatalf("queued %d commands, want %d", len(commands), len(want))
	}
	for _, expected := range want {
		if got := <-commands; got != expected {
			t.Fatalf("command=%+v, want %+v", got, expected)
		}
	}
}
