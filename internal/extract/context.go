package extract

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

	"github.com/SergeAx/scrutus/internal/core"
)

// Stripped is a source file with its comments cut out once, so that a Context
// costs its line budget rather than the size of its scope.
type Stripped struct {
	src  []byte
	text []byte
	cuts []cut
}

// cut is a run of overlapping or adjacent comments, and at is where it was cut
// from text.
type cut struct {
	core.Span
	at int
}

// Strip cuts comments, which must be sorted by Start, out of src.
func Strip(src []byte, comments []core.Span) *Stripped {
	s := &Stripped{src: src, text: make([]byte, 0, len(src))}
	cursor := 0
	for _, c := range comments {
		c.End = min(c.End, len(src))
		if c.End <= cursor {
			continue
		}
		if n := len(s.cuts); n > 0 && c.Start <= cursor {
			s.cuts[n-1].End = c.End
		} else {
			s.text = append(s.text, src[cursor:c.Start]...)
			s.cuts = append(s.cuts, cut{Span: c, at: len(s.text)})
		}
		cursor = c.End
	}
	s.text = append(s.text, src[cursor:]...)
	return s
}

func (s *Stripped) toText(offset int) int {
	i, _ := slices.BinarySearchFunc(s.cuts, offset, func(c cut, offset int) int { return cmp.Compare(c.Start, offset) })
	if i == 0 {
		return offset
	}
	c := s.cuts[i-1]
	return c.at + max(0, offset-c.End)
}

// Context renders scope the way the model reads it: every comment stripped,
// the paired code between marker lines, and at most limit lines kept around
// the code.
func (s *Stripped) Context(scope, code core.Span, limit int) string {
	scope.End = min(scope.End, len(s.src))
	if code.Start < scope.Start || code.End > scope.End {
		return ""
	}

	head := s.text[s.toText(scope.Start):s.toText(code.Start)]
	tail := bytes.TrimRight(s.text[s.toText(code.End):s.toText(scope.End)], "\n")
	if limit > 0 {
		head, tail = lastLines(head, limit), firstLines(tail, limit)
	}

	var b strings.Builder
	b.Write(head)
	b.WriteString(">>> CODE\n")
	b.Write(s.src[code.Start:code.End])
	b.WriteString("\n<<< CODE")
	if len(tail) > 0 {
		b.WriteByte('\n')
		b.Write(tail)
	}
	return capLines(b.String(), bytes.Count(head, []byte("\n")), limit)
}

// lastLines keeps the partial line the code starts on and the n lines above
// it.
func lastLines(text []byte, n int) []byte {
	end := len(text)
	for range n + 1 {
		if end = bytes.LastIndexByte(text[:end], '\n'); end < 0 {
			return text
		}
	}
	return text[end+1:]
}

func firstLines(text []byte, n int) []byte {
	end := -1
	for range n {
		next := bytes.IndexByte(text[end+1:], '\n')
		if next < 0 {
			return text
		}
		end += next + 1
	}
	return text[:end]
}

// capLines keeps the marked code and as much of its surroundings as the line
// budget allows, trimming the far ends first. mark is the line the code starts
// on, which may hold other code before the marker.
func capLines(text string, mark, limit int) string {
	lines := strings.Split(text, "\n")
	if limit <= 0 || len(lines) <= limit {
		return text
	}
	start := max(0, mark-limit/2)
	return strings.Join(lines[start:min(len(lines), start+limit)], "\n")
}
