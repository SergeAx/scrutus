package extract

import (
	"strings"

	"github.com/SergeAx/scrutus/internal/core"
)

// Context renders scope the way the model reads it: every comment stripped,
// the paired code between marker lines, and at most limit lines kept around
// the code. comments must be sorted by Start.
func Context(src []byte, scope, code core.Span, comments []core.Span, limit int) string {
	scope.End = min(scope.End, len(src))
	if code.Start < scope.Start || code.End > scope.End {
		return ""
	}

	var b strings.Builder
	b.Write(strip(src, scope.Start, code.Start, comments))
	b.WriteString(">>> CODE\n")
	b.Write(src[code.Start:code.End])
	b.WriteString("\n<<< CODE\n")
	b.Write(strip(src, code.End, scope.End, comments))
	return capLines(b.String(), limit)
}

func strip(src []byte, start, end int, comments []core.Span) []byte {
	out := make([]byte, 0, max(0, end-start))
	cursor := start
	for _, c := range comments {
		if c.End <= cursor || c.Start >= end {
			continue
		}
		if c.Start > cursor {
			out = append(out, src[cursor:min(c.Start, end)]...)
		}
		cursor = max(cursor, min(c.End, end))
	}
	if cursor < end {
		out = append(out, src[cursor:end]...)
	}
	return out
}

// capLines keeps the marked code and as much of its surroundings as the line
// budget allows, trimming the far ends first.
func capLines(text string, limit int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if limit <= 0 || len(lines) <= limit {
		return strings.Join(lines, "\n")
	}
	mark := 0
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), ">>> CODE") {
			mark = i
			break
		}
	}
	start := max(0, mark-limit/2)
	return strings.Join(lines[start:min(len(lines), start+limit)], "\n")
}
