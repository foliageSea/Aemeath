package replytext

import (
	"errors"
	"strings"
	"testing"
)

func TestGuardRejectsEverySplit(t *testing.T) {
	for _, marker := range markers {
		for _, tag := range []string{marker, strings.ToUpper(marker)} {
			for split := 0; split <= len(tag); split++ {
				var g Guard
				first, err := g.Push("安全正文。" + tag[:split])
				if err == nil {
					var second string
					second, err = g.Push(tag[split:] + " arguments=secret>")
					first += second
				}
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("marker=%q split=%d: %v", tag, split, err)
				}
				if strings.Contains(first, "<") || strings.Contains(first, "secret") {
					t.Fatalf("leaked %q", first)
				}
				if _, err := g.Push("later"); !errors.Is(err, ErrProtocol) {
					t.Fatal("failure not sticky")
				}
				if _, err := g.Finish(); !errors.Is(err, ErrProtocol) {
					t.Fatal("finish not sticky")
				}
			}
		}
	}
}

func TestGuardByteChunksAndNormalText(t *testing.T) {
	for _, text := range []string{"你好，世界。", "1 < 2，3 > 2", "<div>HTML</div>", "a <toolbox> b", "末尾 <", "末尾 </", "提及 DSML 和 tool_calls 字段不是协议"} {
		var g Guard
		var out strings.Builder
		for i := range len(text) {
			part, err := g.Push(text[i : i+1])
			if err != nil {
				t.Fatalf("%q: %v", text, err)
			}
			out.WriteString(part)
		}
		tail, err := g.Finish()
		out.WriteString(tail)
		if err != nil || out.String() != text {
			t.Fatalf("text=%q got=%q err=%v", text, out.String(), err)
		}
	}
	for _, marker := range markers {
		var g Guard
		var out strings.Builder
		var err error
		text := marker + " args=secret>"
		for i := range len(text) {
			var part string
			part, err = g.Push(text[i : i+1])
			out.WriteString(part)
			if err != nil {
				break
			}
		}
		if !errors.Is(err, ErrProtocol) || out.Len() != 0 {
			t.Fatalf("%q leaked=%q err=%v", marker, out.String(), err)
		}
	}
}

func TestGuardIncompleteReservedPrefix(t *testing.T) {
	for _, text := range []string{"<tool_", "</tool_se", "<function_", "<｜｜DSM"} {
		if Validate(text) == nil {
			t.Fatalf("accepted incomplete protocol %q", text)
		}
	}
}
