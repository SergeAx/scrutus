package fix_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SergeAx/scrutus/internal/core"
	_ "github.com/SergeAx/scrutus/internal/extract/golang"
	"github.com/SergeAx/scrutus/internal/fix"
)

const src = `package sample

func f() int {
	// Useless header.
	x := 1
	return x // useless trailer
}
`

func findingFor(t *testing.T, path, comment string) core.Result {
	t.Helper()

	start := strings.Index(src, comment)
	if start < 0 {
		t.Fatalf("%q is not in the fixture", comment)
	}
	return core.Result{
		Action: core.ActionDelete,
		Finding: core.Finding{
			File:        path,
			Comment:     core.Span{Start: start, End: start + len(comment)},
			CommentText: comment,
		},
	}
}

func write(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDeletionTakesTheWholeLineOrJustTheComment(t *testing.T) {
	path := write(t)
	results := []core.Result{
		findingFor(t, path, "// Useless header."),
		findingFor(t, path, "// useless trailer"),
	}

	applied, err := fix.Apply(results, map[string][]byte{path: []byte(src)}, fix.Options{Languages: []string{"go"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) != 1 || applied[0].Err != nil {
		t.Fatalf("applied = %+v", applied)
	}

	edited, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(edited)
	want := `package sample

func f() int {
	x := 1
	return x
}
`
	if got != want {
		t.Errorf("edited file:\n%q\nwant:\n%q", got, want)
	}
}

func TestDiffShowsRemovedLinesWithContext(t *testing.T) {
	spans := []core.Span{
		{Start: strings.Index(src, "// Useless header."), End: strings.Index(src, "// Useless header.") + len("// Useless header.")},
		{Start: strings.Index(src, "// useless trailer"), End: strings.Index(src, "// useless trailer") + len("// useless trailer")},
	}

	diff := fix.UnifiedDiff("sample.go", []byte(src), spans)

	for _, want := range []string{
		"--- a/sample.go",
		"+++ b/sample.go",
		"-\t// Useless header.",
		"-\treturn x // useless trailer",
		"+\treturn x",
		" func f() int {",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff is missing %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "+\t// Useless header.") {
		t.Errorf("a deleted line came back as an addition:\n%s", diff)
	}
}

// A file that stops parsing keeps its original bytes: the whole point of
// re-extracting before the write.
func TestUnparseableResultIsDiscarded(t *testing.T) {
	path := write(t)
	brace := strings.Index(src, "{")
	results := []core.Result{{
		Action: core.ActionDelete,
		Finding: core.Finding{
			File:        path,
			Comment:     core.Span{Start: brace, End: brace + 1},
			CommentText: "{",
		},
	}}

	applied, err := fix.Apply(results, map[string][]byte{path: []byte(src)}, fix.Options{Languages: []string{"go"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) != 1 || applied[0].Err == nil {
		t.Fatalf("applied = %+v, want an error", applied)
	}
	if !strings.Contains(applied[0].Err.Error(), core.RuleFixAborted) {
		t.Errorf("error = %v, want it to name %s", applied[0].Err, core.RuleFixAborted)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != src {
		t.Error("the file was written despite failing to parse")
	}
}
