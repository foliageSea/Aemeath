package wake

import (
	"strings"
	"testing"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

func TestKeywordConversionAndModelVocabulary(t *testing.T) {
	if sum([]byte(keywordVocabulary)) != modelAssets[3].SHA256 {
		t.Fatal("embedded keyword vocabulary diverged from pinned model")
	}
	for _, tc := range []struct{ phrase, want string }{
		{"", KeywordTokens},
		{" 你好小爱 ", KeywordTokens},
		{"你好爱弥斯", "n ǐ h ǎo ài m í s ī @你好爱弥斯\n"},
		{"小雨小雨", "x iǎo y ǔ x iǎo y ǔ @小雨小雨\n"},
		{"小智同学", "x iǎo zh ì t óng x ué @小智同学\n"},
		{"小绿小绿", "x iǎo l ǜ x iǎo l ǜ @小绿小绿\n"},
		{"去玩", "q ù w án @去玩\n"},
		{"安安", "ān ān @安安\n"},
	} {
		t.Run(tc.phrase, func(t *testing.T) {
			got, err := TokensForPhrase(tc.phrase)
			if err != nil || got != tc.want {
				t.Fatalf("tokens=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestWakePhraseValidation(t *testing.T) {
	for _, phrase := range []string{"爱", strings.Repeat("爱", 13), "你好 小爱", "你好，小爱", "小爱\n小爱", "hello", "小爱1", "小爱@", "小爱/", "小爱😀", "\xff小爱"} {
		t.Run(phrase, func(t *testing.T) {
			if _, err := NormalizePhrase(phrase); err == nil {
				t.Fatalf("accepted invalid wake phrase %q", phrase)
			}
			if _, err := TokensForPhrase(phrase); err == nil {
				t.Fatalf("built keyword grammar for invalid phrase %q", phrase)
			}
		})
	}
	// Find an actual missing dictionary entry rather than assuming that a rare
	// character is unsupported (the dictionary includes many extension blocks).
	for char := rune(0x4e00); char <= 0x323af; char++ {
		if _, exists := pinyin.PinyinDict[int(char)]; unicode.Is(unicode.Han, char) && !exists {
			if _, err := NormalizePhrase("你好" + string(char)); err == nil {
				t.Fatalf("accepted Han character without pronunciation: %U", char)
			}
			break
		}
	}
	cfg, err := normalize(Config{Phrase: " 小智同学 "})
	if err != nil || cfg.Phrase != "小智同学" {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}
