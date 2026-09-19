package extract

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/SergeAx/scrutus/internal/core"
)

// SplitDocblock turns a `/** */` block into a finding for its prose and one
// per tag line, so a redundant @var is graded on its own and a useful summary
// is not dragged down with it. base carries the pairing and the block's own
// span; every finding returned shares one Block, and with it one request.
func SplitDocblock(base core.Finding, text string) []core.Finding {
	prose, proseEnd, tags := splitDocblock(text)
	start := base.Comment.Start
	blockKey := base.File + ":" + strconv.Itoa(start)

	var out []core.Finding
	if strings.TrimSpace(strings.Trim(prose, "/* \t\n")) != "" {
		summary := base
		summary.CommentText = prose
		summary.Comment = core.Span{Start: start, End: start + proseEnd}
		summary.Block = blockKey
		out = append(out, summary)
	}
	for i, t := range tags {
		tag := base
		tag.Kind = core.KindAnnotation
		tag.CommentText = t.text
		tag.Comment = core.Span{Start: start + t.offset, End: start + t.offset + len(t.text)}
		tag.Line = base.Line + t.lineOffset
		tag.Block = blockKey
		tag.Slot = "a" + strconv.Itoa(i+1)
		tag.Protected = false
		out = append(out, tag)
	}
	return out
}

type tag struct {
	text       string
	offset     int
	lineOffset int
}

var tagLine = regexp.MustCompile(`^\s*(?:/\*\*?)?\s*\*?\s*(@[A-Za-z][\w-]*)`)

func splitDocblock(text string) (prose string, proseEnd int, tags []tag) {
	lines := strings.Split(text, "\n")
	var free []string
	offset, proseEnd := 0, len(text)
	for i, line := range lines {
		if tagLine.MatchString(line) {
			trimmed := strings.TrimLeft(line, " \t*/")
			if len(tags) == 0 {
				proseEnd = offset
			}
			tags = append(tags, tag{
				text:       strings.TrimRight(strings.TrimSuffix(strings.TrimRight(trimmed, " \t\r"), "*/"), " \t"),
				offset:     offset + (len(line) - len(trimmed)),
				lineOffset: i,
			})
		} else {
			free = append(free, line)
		}
		offset += len(line) + 1
	}
	return strings.Join(free, "\n"), proseEnd, tags
}
