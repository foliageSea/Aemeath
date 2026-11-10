package voice

import (
	"strings"
	"unicode"

	"aemeath/agent/internal/wake"
	"github.com/mozillazg/go-pinyin"
)

// Remove only the leading wake invocation, never a phrase inside a question.
// ASR may transcribe a custom name with different characters or tones, such as
// 爱弥斯 -> 艾米斯. Match full syllables without tones, not fuzzy text substrings.
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
		if i >= len(runes) || !sameWakeSyllable(runes[i], want) {
			return text
		}
		i++
	}
	for i < len(runes) && (unicode.IsSpace(runes[i]) || unicode.IsPunct(runes[i])) {
		i++
	}
	return string(runes[i:])
}

func sameWakeSyllable(got, want rune) bool {
	if got == want {
		return true
	}
	if !unicode.Is(unicode.Han, got) || !unicode.Is(unicode.Han, want) {
		return false
	}
	// Use the same dictionary default reading as the offline wake model. Do not
	// accept every polyphonic reading, which could remove unrelated commands.
	args := pinyin.Args{Style: pinyin.Normal, Heteronym: false, Fallback: func(rune, pinyin.Args) []string { return nil }}
	left, right := pinyin.SinglePinyin(got, args), pinyin.SinglePinyin(want, args)
	return len(left) == 1 && len(right) == 1 && left[0] != "" && left[0] == right[0]
}
