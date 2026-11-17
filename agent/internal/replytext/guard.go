// Package replytext keeps serialized tool protocols out of user-facing replies.
package replytext

import (
	"errors"
	"strings"
)

// ErrProtocol deliberately contains no provider output or tool arguments.
var ErrProtocol = errors.New("模型返回了工具协议文本，已停止输出；请检查模型接口兼容性 [tool_protocol]")

// These are reserved wire-format prefixes, not general XML/HTML. Reject rather
// than execute or strip them: stripping could turn a failed call into a fake answer.
var markers = []string{
	"<｜DSML", "<｜｜DSML", "<|DSML", "<||DSML",
	"</｜DSML", "</｜｜DSML", "</|DSML", "</||DSML",
	"<tool_search", "</tool_search", "<tool_call", "</tool_call",
	"<function_call", "</function_call",
}

// Guard holds only the longest suffix which could start a reserved marker.
// It must run BEFORE display, synthesis, and history commit. The zero value is ready.
// Safe text preceding a later protocol may already have streamed; protocol bytes
// themselves are never released. Call Finish only on a successful end of stream.
type Guard struct {
	pending string
	failed  bool
}

func (g *Guard) Push(delta string) (string, error) {
	if g.failed {
		return "", ErrProtocol
	}
	text := g.pending + delta
	lower := asciiLower(text)
	keep := 0
	for _, marker := range markers {
		marker = asciiLower(marker)
		if strings.Contains(lower, marker) {
			g.failed = true
			g.pending = ""
			return "", ErrProtocol
		}
		for n := 1; n < len(marker) && n <= len(lower); n++ {
			if strings.HasSuffix(lower, marker[:n]) && n > keep {
				keep = n
			}
		}
	}
	g.pending = text[len(text)-keep:]
	return text[:len(text)-keep], nil
}

func (g *Guard) Finish() (string, error) {
	if g.failed {
		return "", ErrProtocol
	}
	tail := g.pending
	g.pending = ""
	// Incomplete but unambiguous reserved prefixes are also a protocol failure.
	lower := asciiLower(tail)
	if strings.Contains(lower, "dsm") || strings.HasPrefix(lower, "<tool_") || strings.HasPrefix(lower, "</tool_") || strings.HasPrefix(lower, "<function_") || strings.HasPrefix(lower, "</function_") {
		g.failed = true
		return "", ErrProtocol
	}
	return tail, nil
}

// Validate is the whole-response equivalent, used before non-streaming commits
// and for replay/preview. It does not alter the supplied text.
func Validate(text string) error {
	var g Guard
	if _, err := g.Push(text); err != nil {
		return err
	}
	_, err := g.Finish()
	return err
}

// ASCII folding keeps byte offsets intact, including split UTF-8 sequences.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
