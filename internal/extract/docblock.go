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

	blockText := ""
	if len(tags) > 0 {
		blockText = marked(text, tags)
	}

	var out []core.Finding
	if strings.TrimSpace(strings.Trim(prose, "/* \t\n")) != "" {
		summary := base
		summary.CommentText = prose
		summary.Comment = core.Span{Start: start, End: start + proseEnd}
		summary.Block = blockKey
		summary.BlockText = blockText
		out = append(out, summary)
	}
	for i, t := range tags {
		tag := base
		tag.Kind = core.KindAnnotation
		tag.CommentText = t.text
		tag.Comment = core.Span{Start: start + t.offset, End: start + t.offset + len(t.text)}
		tag.Line = base.Line + t.lineOffset
		tag.Block = blockKey
		tag.BlockText = blockText
		tag.Slot = slot(i)
		tag.Protected = false
		out = append(out, tag)
	}
	return out
}

func slot(i int) string { return "a" + strconv.Itoa(i+1) }

// marked encloses each tag's lines in its slot markers.
func marked(text string, tags []tag) string {
	opens, closes := map[int]string{}, map[int]string{}
	for i, t := range tags {
		opening, closing := core.SlotMarkers(slot(i))
		opens[t.lineOffset] = opening
		closes[t.lineOffset+strings.Count(t.text, "\n")] = closing
	}
	var lines []string
	for i, line := range strings.Split(text, "\n") {
		if opening, ok := opens[i]; ok {
			lines = append(lines, opening)
		}
		lines = append(lines, line)
		if closing, ok := closes[i]; ok {
			lines = append(lines, closing)
		}
	}
	return strings.Join(lines, "\n")
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
	continued := false
	for i, line := range lines {
		end := offset + len(strings.TrimRight(strings.TrimSuffix(strings.TrimRight(line, " \t\r"), "*/"), " \t"))
		switch {
		case tagLine.MatchString(line):
			if len(tags) == 0 {
				proseEnd = offset
			}
			start := offset + len(line) - len(strings.TrimLeft(line, " \t*/"))
			tags = append(tags, tag{text: text[start:end], offset: start, lineOffset: i})
			continued = true
		case continued && strings.TrimLeft(text[offset:end], " \t*/") != "":
			// A tag's description runs on to the next tag or blank line.
			last := &tags[len(tags)-1]
			last.text = text[last.offset:end]
		default:
			free = append(free, line)
			continued = false
		}
		offset += len(line) + 1
	}
	return strings.Join(free, "\n"), proseEnd, tags
}
