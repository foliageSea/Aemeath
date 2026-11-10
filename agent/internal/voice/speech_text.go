package voice

import "strings"

// speechTextFilter removes numeric citations and numbered source lines before
// sentence splitting. It buffers only a possible marker, so ordinary prose can
// reach synthesis immediately, even when transport deltas split a marker.
// Display and speech share this filter; conversation memory retains the original.
type speechTextFilter struct {
	marker        string
	lineHasText   bool
	markerAtStart bool
	skipLine      bool
}

func cleanResponseText(text string) string {
	var filter speechTextFilter
	return strings.TrimSpace(filter.Push(text) + filter.Finish())
}

func (f *speechTextFilter) Push(delta string) string {
	var out strings.Builder
	for i := 0; i < len(delta); i++ {
		c := delta[i]
		if c == '\n' || c == '\r' {
			if !f.skipLine {
				out.WriteString(f.marker)
				out.WriteByte(c)
			}
			f.marker = ""
			f.lineHasText = false
			f.skipLine = false
			continue
		}
		if f.skipLine {
			continue
		}
		if f.marker != "" {
			if c >= '0' && c <= '9' {
				f.marker += string(c)
				continue
			}
			if c == ']' && len(f.marker) > 1 {
				f.marker = ""
				f.skipLine = f.markerAtStart
				continue
			}
			out.WriteString(f.marker)
			f.marker = ""
			f.lineHasText = true
		}
		if c == '[' {
			f.marker = "["
			f.markerAtStart = !f.lineHasText
			continue
		}
		out.WriteByte(c)
		if c != ' ' && c != '	' {
			f.lineHasText = true
		}
	}
	return out.String()
}

func (f *speechTextFilter) Finish() string {
	tail := f.marker
	f.marker = ""
	return tail
}
