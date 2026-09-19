package golang_test

import (
	"strings"
	"testing"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
	_ "github.com/SergeAx/scrutus/internal/extract/golang"
)

const src = `package sample

// Exported is documented here.
func Exported(a int) int {
	// Double it, because the caller halved it upstream.
	b := a * 2
	c := b + 1

	// Trailing block comment.
	return c // the answer
}

func unexported() string {
	// Head of a paragraph.
	first := "a"
	second := "b"

	third := "c"
	return first + second + third
}
`

func extractAll(t *testing.T) map[string]core.Finding {
	t.Helper()

	findings, err := extract.Extract("sample.go", []byte(src), []string{"go"}, extract.Options{ContextLines: 20})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	byComment := map[string]core.Finding{}
	for _, f := range findings {
		byComment[strings.TrimSpace(f.CommentText)] = f
	}
	return byComment
}

func TestPairingRules(t *testing.T) {
	findings := extractAll(t)

	cases := []struct {
		comment   string
		kind      core.Kind
		codeHas   string
		protected bool
	}{
		{"// Exported is documented here.", core.KindDoc, "func Exported(a int) int {", true},
		{"// Double it, because the caller halved it upstream.", core.KindInline, "b := a * 2", false},
		{"// Trailing block comment.", core.KindInline, "return c", false},
		{"// the answer", core.KindTrailing, "return c", false},
		{"// Head of a paragraph.", core.KindInline, `first := "a"`, false},
	}
	for _, tc := range cases {
		f, ok := findings[tc.comment]
		if !ok {
			t.Errorf("%q was not extracted", tc.comment)
			continue
		}
		if f.Kind != tc.kind {
			t.Errorf("%q: kind %s, want %s", tc.comment, f.Kind, tc.kind)
		}
		if !strings.Contains(f.CodeText, tc.codeHas) {
			t.Errorf("%q: code %q does not contain %q", tc.comment, f.CodeText, tc.codeHas)
		}
		if f.Protected != tc.protected {
			t.Errorf("%q: protected %v, want %v", tc.comment, f.Protected, tc.protected)
		}
	}
}

// The paragraph rule stops at a blank line, so a comment heading one chunk
// never claims the next.
func TestParagraphStopsAtBlankLine(t *testing.T) {
	f, ok := extractAll(t)["// Head of a paragraph."]
	if !ok {
		t.Fatal("comment not extracted")
	}
	if !strings.Contains(f.CodeText, `second := "b"`) {
		t.Errorf("paragraph missed the second statement: %q", f.CodeText)
	}
	if strings.Contains(f.CodeText, `third := "c"`) {
		t.Errorf("paragraph ran past the blank line: %q", f.CodeText)
	}
}

func TestSpansAreExactByteOffsets(t *testing.T) {
	for comment, f := range extractAll(t) {
		if got := src[f.Comment.Start:f.Comment.End]; strings.TrimSpace(got) != comment {
			t.Errorf("span %d-%d reads %q, want %q", f.Comment.Start, f.Comment.End, got, comment)
		}
	}
}

// Context marks the paired code and strips every comment, so neighbouring
// comments cannot sway a verdict.
func TestContextMarksCodeAndDropsComments(t *testing.T) {
	f, ok := extractAll(t)["// Double it, because the caller halved it upstream."]
	if !ok {
		t.Fatal("comment not extracted")
	}
	if !strings.Contains(f.Context, ">>> CODE") || !strings.Contains(f.Context, "<<< CODE") {
		t.Errorf("context carries no markers:\n%s", f.Context)
	}
	if strings.Contains(f.Context, "// the answer") {
		t.Errorf("context kept a neighbouring comment:\n%s", f.Context)
	}
}

func TestMissingExtractorIsAnError(t *testing.T) {
	_, err := extract.Extract("app.php", []byte("<?php // x"), []string{"go", "php"}, extract.Options{})
	var missing *extract.MissingExtractorError
	if err == nil {
		t.Fatal("a configured language with no extractor was skipped silently")
	}
	if !asMissing(err, &missing) {
		t.Fatalf("error = %v, want MissingExtractorError", err)
	}
	if missing.Language != "php" {
		t.Errorf("language = %q, want php", missing.Language)
	}
}

// A language nobody configured is simply out of scope.
func TestUnconfiguredLanguageIsSkipped(t *testing.T) {
	findings, err := extract.Extract("app.php", []byte("<?php // x"), []string{"go"}, extract.Options{})
	if err != nil || findings != nil {
		t.Errorf("findings = %v, err = %v; want no findings and no error", findings, err)
	}
}

func asMissing(err error, target **extract.MissingExtractorError) bool {
	if e, ok := err.(*extract.MissingExtractorError); ok {
		*target = e
		return true
	}
	return false
}
