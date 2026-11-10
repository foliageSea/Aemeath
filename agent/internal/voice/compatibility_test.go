package voice

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestZeroTurnIsExplicitForFirmwareHandshake(t *testing.T) {
	b, err := json.Marshal(message{V: 1, Type: "state", Epoch: 42, State: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["turn"]) != "0" {
		t.Fatalf("firmware requires numeric turn even for initial idle: %s", b)
	}
}
func TestDeviceCaptionsFitRXLimitAndPreserveUTF8(t *testing.T) {
	for _, s := range []string{strings.Repeat("爱弥斯你好", 1000), strings.Repeat("", 9000), strings.Repeat("<>&\"", 2000)} {
		caption := deviceCaption(s)
		if !utf8.ValidString(caption) {
			t.Fatal("caption split a UTF8 character")
		}
		b, err := json.Marshal(message{V: 1, Type: "error", Epoch: 42, Turn: 1, Text: caption, Message: caption})
		// An individual caption/error field is512bytes; an error never also carries Text.
		if err != nil {
			t.Fatal(err)
		}
		var both map[string]any
		json.Unmarshal(b, &both)
		delete(both, "text")
		single, _ := json.Marshal(both)
		if len(single) > 4096 {
			t.Fatalf("caption exceeds device RX limit: %d", len(single))
		}
	}
}
func TestStripOnlyLeadingWakeInvocation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"你好，小爱，帮我看看CPU", "帮我看看CPU"}, {"  你好小爱！ ", ""}, {"你好，小爱吃什么？", "吃什么？"},
		{"请解释你好小爱这个词", "请解释你好小爱这个词"}, {"你好，小艾，看看CPU", "看看CPU"}, {"你好，世界", "你好，世界"},
	} {
		if got := stripWakePrefix(tc.in); got != tc.want {
			t.Fatalf("%q => %q want %q", tc.in, got, tc.want)
		}
	}
}
