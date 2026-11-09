package voice

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func collectSpeechSegments(t *testing.T, chunks []string) []string {
	t.Helper()
	var s sentenceSegmenter
	var out []string
	for _, chunk := range chunks {
		segments, err := s.Push(chunk)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, segments...)
	}
	tail, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, tail...)
	if strings.Join(out, "") != strings.Join(chunks, "") {
		t.Fatalf("input lost or duplicated: %#v", out)
	}
	if len(out) > 8 {
		t.Fatalf("more than eight segments: %d", len(out))
	}
	for _, segment := range out {
		if segment == "" || !utf8.ValidString(segment) {
			t.Fatalf("empty/invalid segment %q", segment)
		}
	}
	return out
}

func TestSentenceSegmenterChineseFirstClauseAndLargerFollowingSegments(t *testing.T) {
	first := "今天的天气非常适合出门散步，我可以陪你安排一个轻松的下午。"
	later := strings.Repeat("我们还可以沿着河边慢慢走，欣赏傍晚的风景。", 5)
	text := first + later + "记得带水。"
	chunks := []string{}
	for _, r := range text {
		chunks = append(chunks, string(r))
	}
	segments := collectSpeechSegments(t, chunks)
	if len(segments) < 3 {
		t.Fatalf("not segmented: %#v", segments)
	}
	n := utf8.RuneCountInString(segments[0])
	if n < 20 || n > 40 {
		t.Fatalf("first clause length %d: %q", n, segments[0])
	}
	for _, segment := range segments[1 : len(segments)-1] {
		if n := utf8.RuneCountInString(segment); n < 60 || n > 100 {
			t.Fatalf("following clause length %d: %q", n, segment)
		}
	}
}

func TestSentenceSegmenterEmitsCompleteDeltaWithoutWaitingForFinish(t *testing.T) {
	var s sentenceSegmenter
	out, err := s.Push("你好，我是爱弥斯。")
	if err != nil || !reflect.DeepEqual(out, []string{"你好，我是爱弥斯。"}) {
		t.Fatalf("complete initial delta held: %#v %v", out, err)
	}
	var quoted sentenceSegmenter
	out, err = quoted.Push("她说：“你好，朋友！")
	if err != nil || len(out) != 0 {
		t.Fatalf("unclosed quote emitted: %#v %v", out, err)
	}
	out, err = quoted.Push("”")
	if err != nil || !reflect.DeepEqual(out, []string{"她说：“你好，朋友！”"}) {
		t.Fatalf("closed quote held: %#v %v", out, err)
	}
}

func TestSentenceSegmenterFirstStrongSentenceAndTrivialFragments(t *testing.T) {
	var s sentenceSegmenter
	out, err := s.Push("你好。接下来")
	if err != nil || !reflect.DeepEqual(out, []string{"你好。"}) {
		t.Fatalf("short complete sentence not emitted: %#v %v", out, err)
	}
	var trivial sentenceSegmenter
	out, err = trivial.Push("嗯。")
	if err != nil || len(out) != 0 {
		t.Fatalf("trivial fragment emitted: %#v %v", out, err)
	}
	out, err = trivial.Push("我来为你解释。现在")
	if err != nil || len(out) != 1 || out[0] != "嗯。我来为你解释。" {
		t.Fatalf("fragment was not coalesced: %#v %v", out, err)
	}
}

func TestSentenceSegmenterQuotesNumbersAndEnglishPeriods(t *testing.T) {
	cases := []string{
		"她说：“你好，朋友！”然后我们讨论接下来的安排。",
		"The price is $47.69 and pi is 3.14. Version v2.5.1 is ready! Thank you.",
		"Dr. Smith has v2.5 installed. It costs $47.69 today, including tax.",
		strings.Repeat("甲", 36) + "v2.5.1，随后我们继续。",
		strings.Repeat("乙", 37) + "$47.69，然后我们继续。",
		"第一行内容已经足够完整。\n第二行保留原来的空格。  尾巴",
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			// Every possible transport boundary, including inside a multi-byte rune.
			for split := 0; split <= len(text); split++ {
				parts := collectSpeechSegments(t, []string{text[:split], text[split:]})
				for _, token := range []string{"$47.69", "3.14", "v2.5.1", "v2.5", "Dr."} {
					if !strings.Contains(text, token) {
						continue
					}
					found := false
					for _, part := range parts {
						if strings.Contains(part, token) {
							found = true
						}
					}
					if !found {
						t.Fatalf("split indivisible token %q at byte %d: %#v", token, split, parts)
					}
				}
			}
		})
	}
	parts := collectSpeechSegments(t, []string{"她说：“你好，朋友！", "”然后"})
	if len(parts) != 2 || parts[0] != "她说：“你好，朋友！”" {
		t.Fatalf("closing quote detached: %#v", parts)
	}
}

func TestSentenceSegmenterPreservesPartialUTF8AndFinalTail(t *testing.T) {
	text := "  你好，世界👩‍🚀！ 这是 é 和组合字符。尾巴没有标点  "
	var chunks []string
	for i := range len(text) {
		chunks = append(chunks, text[i:i+1])
	}
	collectSpeechSegments(t, chunks)
	var s sentenceSegmenter
	if out, err := s.Push("尚未完成"); err != nil || len(out) != 0 {
		t.Fatalf("premature tail: %#v %v", out, err)
	}
	out, err := s.Finish()
	if err != nil || !reflect.DeepEqual(out, []string{"尚未完成"}) {
		t.Fatalf("tail missing: %#v %v", out, err)
	}
	if out, err = s.Finish(); err != nil || len(out) != 0 {
		t.Fatalf("Finish duplicated: %#v %v", out, err)
	}
	if _, err = s.Push("额外"); err == nil {
		t.Fatal("Push after Finish accepted")
	}
}

func TestSentenceSegmenterBoundsAndCoalescedFinalRemainder(t *testing.T) {
	text := strings.Repeat("这是一个完整的测试句子，我们会把所有内容按顺序保留下来。", 100)
	var chunks []string
	for _, r := range text {
		chunks = append(chunks, string(r))
	}
	parts := collectSpeechSegments(t, chunks)
	if len(parts) != 8 {
		t.Fatalf("expected eight bounded segments, got %d", len(parts))
	}
	if utf8.RuneCountInString(parts[7]) <= 100 {
		t.Fatal("last slot was not reserved for coalesced remainder")
	}
	plain := strings.Repeat("长", 4000)
	parts = collectSpeechSegments(t, []string{plain})
	if len(parts) != 8 || utf8.RuneCountInString(parts[0]) != 40 || utf8.RuneCountInString(parts[1]) != 100 {
		t.Fatalf("long unpunctuated speech not bounded: %d", len(parts))
	}
	var s sentenceSegmenter
	accepted := strings.Repeat("x", 16384)
	out, err := s.Push(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Push("!"); err == nil {
		t.Fatal("input byte limit ignored")
	}
	if _, err = s.Finish(); err == nil {
		t.Fatal("overflow not terminal")
	}
	if len(out) != 0 {
		t.Fatal("indivisible token cut before end")
	}
	exact := collectSpeechSegments(t, []string{accepted})
	if len(exact) != 1 {
		t.Fatal("long token split")
	}
	// The limit counts text already emitted as well as the buffered remainder.
	var emitted sentenceSegmenter
	if _, err = emitted.Push("你好。" + strings.Repeat("a", 16375)); err != nil {
		t.Fatal(err)
	}
	if _, err = emitted.Push("x"); err == nil {
		t.Fatal("emitted bytes not counted")
	}
}

func TestSentenceSegmenterRejectsMalformedUTF8(t *testing.T) {
	for _, input := range []string{string([]byte{0xff}), string([]byte{0xc0, 0x80})} {
		var s sentenceSegmenter
		if _, err := s.Push(input); err == nil {
			t.Fatal("accepted malformed UTF8")
		}
	}
	var s sentenceSegmenter
	if _, err := s.Push(string([]byte{0xe4, 0xbd})); err != nil {
		t.Fatal("partial UTF8 prematurely rejected", err)
	}
	if _, err := s.Finish(); err == nil {
		t.Fatal("incomplete final rune accepted")
	}
}

func TestSentenceSegmenterSoftPauseNewlineAndChunkOrder(t *testing.T) {
	text := strings.Repeat("甲", 23) + "，" + strings.Repeat("乙", 65) + "；" + strings.Repeat("丙", 65) + "\n结尾。"
	parts := collectSpeechSegments(t, []string{text})
	if len(parts) < 3 || !strings.HasSuffix(parts[0], "，") || !strings.HasSuffix(parts[1], "；") {
		t.Fatalf("soft pauses ignored: %#v", parts)
	}
	// Chunk boundaries must never affect the text or segment-count guarantees.
	for size := 1; size <= 11; size++ {
		var chunks []string
		for i := 0; i < len(text); i += size {
			chunks = append(chunks, text[i:min(i+size, len(text))])
		}
		collectSpeechSegments(t, chunks)
	}
}
