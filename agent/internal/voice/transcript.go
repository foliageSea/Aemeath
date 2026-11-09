package voice

import (
	"strings"
	"unicode"
)

// Remove only the leading wake invocation, never a quoted phrase in a question.
func stripWakePrefix(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	target := []rune("你好小爱")
	i := 0
	for _, want := range target {
		for i < len(runes) && (unicode.IsSpace(runes[i]) || unicode.IsPunct(runes[i])) {
			i++
		}
		if i >= len(runes) || runes[i] != want {
			return text
		}
		i++
	}
	for i < len(runes) && (unicode.IsSpace(runes[i]) || unicode.IsPunct(runes[i])) {
		i++
	}
	return string(runes[i:])
}
