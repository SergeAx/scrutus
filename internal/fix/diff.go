package fix

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/SergeAx/scrutus/internal/core"
)

const diffContext = 3

// UnifiedDiff builds the patch from the spans that were removed. fix only ever
// deletes, so the hunks follow from the edit plan and need no diffing.
func UnifiedDiff(path string, src []byte, spans []core.Span) string {
	lines := splitLines(src)
	changed := make([]*editedLine, len(lines))
	for i, line := range lines {
		for _, span := range spans {
			if span.End <= line.start || span.Start >= line.end {
				continue
			}
			if changed[i] == nil {
				changed[i] = &editedLine{text: line.text}
			}
			changed[i].cut(line.start, span)
		}
	}

	var b strings.Builder
	slashed := filepath.ToSlash(path)
	for _, h := range hunks(changed, len(lines), diffContext) {
		if b.Len() == 0 {
			fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", slashed, slashed)
		}
		writeHunk(&b, lines, changed, h)
	}
	return b.String()
}

type sourceLine struct {
	text       string
	start, end int
}

type editedLine struct {
	text    string
	removed bool
}

// cut removes the span's bytes from this line; a line the span covers whole
// disappears rather than turning into an empty one.
func (e *editedLine) cut(lineStart int, span core.Span) {
	from := max(0, span.Start-lineStart)
	to := min(len(e.text), span.End-lineStart)
	if from <= 0 && to >= len(e.text) {
		e.removed = true
		return
	}
	if from < to {
		e.text = e.text[:from] + e.text[to:]
	}
	if strings.TrimSpace(e.text) == "" {
		e.removed = true
	}
}

func splitLines(src []byte) []sourceLine {
	var lines []sourceLine
	start := 0
	for i := 0; i <= len(src); i++ {
		if i < len(src) && src[i] != '\n' {
			continue
		}
		if i == len(src) && start == i {
			break
		}
		text := strings.TrimRight(string(src[start:min(i, len(src))]), "\r")
		lines = append(lines, sourceLine{text: text, start: start, end: i + 1})
		start = i + 1
	}
	return lines
}

type hunk struct{ from, to int }

func hunks(changed []*editedLine, total, context int) []hunk {
	var out []hunk
	for i := 0; i < total; i++ {
		if changed[i] == nil {
			continue
		}
		last := i
		for j := i; j < total; j++ {
			if changed[j] != nil {
				last = j
			} else if j-last > context*2 {
				break
			}
		}
		current := hunk{from: max(0, i-context), to: min(total, last+context+1)}
		if len(out) > 0 && current.from <= out[len(out)-1].to {
			out[len(out)-1].to = current.to
		} else {
			out = append(out, current)
		}
		i = last
	}
	return out
}

func writeHunk(b *strings.Builder, lines []sourceLine, changed []*editedLine, h hunk) {
	before, after := 0, 0
	for i := h.from; i < h.to; i++ {
		before++
		if edited := changed[i]; edited == nil || !edited.removed {
			after++
		}
	}
	fmt.Fprintf(b, "@@ -%d,%d +%d,%d @@\n", h.from+1, before, h.from+1, after)

	for i := h.from; i < h.to; i++ {
		edited := changed[i]
		switch {
		case edited == nil:
			fmt.Fprintf(b, " %s\n", lines[i].text)
		case edited.removed:
			fmt.Fprintf(b, "-%s\n", lines[i].text)
		default:
			fmt.Fprintf(b, "-%s\n+%s\n", lines[i].text, edited.text)
		}
	}
}
