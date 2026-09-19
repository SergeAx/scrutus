package scrutus_test

import (
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func parses(t *testing.T, src []byte) bool {
	t.Helper()
	_, err := parser.ParseFile(token.NewFileSet(), "edited.go", src, parser.ParseComments)
	if err != nil {
		t.Logf("parse: %v", err)
	}
	return err == nil
}

// outsideCI lets a test exercise fix's writes when the suite itself runs in
// CI, where fix refuses to write.
func outsideCI(t *testing.T) {
	t.Helper()
	for _, name := range []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI"} {
		t.Setenv(name, "")
	}
}
