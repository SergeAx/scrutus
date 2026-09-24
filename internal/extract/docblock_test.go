package extract_test

import (
	"strings"
	"testing"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
)

func TestSplitDocblockKeepsATagsContinuationLines(t *testing.T) {
	const at = 100
	text := "/**\n" +
		" * Caches decoded entries.\n" +
		" *\n" +
		" * @var object[] decoded entries by id; one attach reads several\n" +
		" *               fields off an entry\n" +
		" * @see Other\n" +
		" */"
	got := extract.SplitDocblock(core.Finding{File: "a.php", Comment: core.Span{Start: at, End: at + len(text)}}, text)

	want := []string{
		"/**\n * Caches decoded entries.\n *\n */",
		"@var object[] decoded entries by id; one attach reads several\n *               fields off an entry",
		"@see Other",
	}
	if len(got) != len(want) {
		t.Fatalf("%d findings, want %d: %+v", len(got), len(want), got)
	}
	for i, f := range got {
		if f.CommentText != want[i] {
			t.Errorf("finding %d: comment %q, want %q", i, f.CommentText, want[i])
		}
	}
	for _, f := range got[1:] {
		if spanned := text[f.Comment.Start-at : f.Comment.End-at]; spanned != f.CommentText {
			t.Errorf("span of %q covers %q", f.CommentText, spanned)
		}
	}
	if strings.Contains(got[0].CommentText, "fields off") {
		t.Errorf("the prose took the tag's continuation: %q", got[0].CommentText)
	}
}
