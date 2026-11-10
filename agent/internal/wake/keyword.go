package wake

import (
	_ "embed"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mozillazg/go-pinyin"
)

// This is the token vocabulary of the pinned WenetSpeech model, not an
// independently editable keyword file. Validate before saving configuration so
// unsupported pronunciations never disable an otherwise working detector.
//
//go:embed keyword_vocabulary.txt
var keywordVocabulary string

var keywordTokenSet = func() map[string]bool {
	result := make(map[string]bool)
	for _, line := range strings.Split(keywordVocabulary, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			result[fields[0]] = true
		}
	}
	return result
}()

// NormalizePhrase defaults an empty value and accepts a single Chinese phrase.
// Readings are deterministic dictionary defaults; no network or runtime Python
// installation is needed. Phrase-specific polyphonic disambiguation is not used.
func NormalizePhrase(phrase string) (string, error) {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		phrase = Keyword
	}
	if !utf8.ValidString(phrase) || utf8.RuneCountInString(phrase) < 2 || utf8.RuneCountInString(phrase) > 12 {
		return "", fmt.Errorf("唤醒词需要包含 2–12 个中文汉字")
	}
	if _, err := phraseTokens(phrase); err != nil {
		return "", err
	}
	return phrase, nil
}

// TokensForPhrase uses the model's ppinyin format: retain y/w initials and
// written finals (e.g. 雨 => y ǔ, 去 => q ù), matching sherpa-onnx strict=False.
func TokensForPhrase(phrase string) (string, error) {
	phrase, err := NormalizePhrase(phrase)
	if err != nil {
		return "", err
	}
	tokens, err := phraseTokens(phrase)
	if err != nil {
		return "", err
	}
	return strings.Join(tokens, " ") + " @" + phrase + "\n", nil
}

func phraseTokens(phrase string) ([]string, error) {
	var result []string
	args := pinyin.Args{Style: pinyin.Tone, Heteronym: false, Fallback: func(rune, pinyin.Args) []string { return nil }}
	for _, char := range phrase {
		if !unicode.Is(unicode.Han, char) {
			return nil, fmt.Errorf("唤醒词仅支持中文汉字，不支持空格、标点、数字或英文")
		}
		readings := pinyin.SinglePinyin(char, args)
		if len(readings) != 1 || readings[0] == "" {
			return nil, fmt.Errorf("唤醒词中的「%c」没有可用的中文读音", char)
		}
		syllable := readings[0]
		var parts []string
		for _, initial := range []string{"zh", "ch", "sh", "b", "p", "m", "f", "d", "t", "n", "l", "g", "k", "h", "j", "q", "x", "r", "z", "c", "s", "y", "w"} {
			if strings.HasPrefix(syllable, initial) {
				parts = append(parts, initial)
				syllable = strings.TrimPrefix(syllable, initial)
				break
			}
		}
		if syllable != "" {
			parts = append(parts, syllable)
		}
		for _, part := range parts {
			if !keywordTokenSet[part] {
				return nil, fmt.Errorf("当前离线模型不支持「%c」的读音，请更换唤醒词", char)
			}
		}
		result = append(result, parts...)
	}
	return result, nil
}
