package voice

import (
	"strings"
	"unicode"

	"aemeath/agent/internal/wake"
)

// Remove only the leading wake invocation, never a quoted phrase in a question.
func stripWakePrefix(text string) string {
	return stripWakePhrasePrefix(text, wake.Keyword)
}

func stripWakePhrasePrefix(text, phrase string) string {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		phrase = wake.Keyword
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	target := []rune(phrase)
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
