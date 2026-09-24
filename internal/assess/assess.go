// Package assess defines the scoring boundary: the rubric that phrases the
// questions and the Assessor that answers them.
package assess

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/SergeAx/scrutus/internal/core"
)

//go:embed rubric_default.toml
var defaultRubric []byte

const Levels = 4

type Axis struct {
	Instructions string   `toml:"instructions"`
	Criteria     []string `toml:"criteria"`
}

type Rubric struct {
	Version      int  `toml:"rubric_version"`
	Accuracy     Axis `toml:"accuracy"`
	Usefulness   Axis `toml:"usefulness"`
	Overreach    Axis `toml:"overreach"`
	CommentedOut Axis `toml:"commented_out"`
}

// LoadRubric reads the embedded default or a file, which may reword anything
// but must keep exactly four levels per axis so thresholds stay comparable.
func LoadRubric(path string) (Rubric, error) {
	raw := defaultRubric
	if path != "" && path != "default" {
		content, err := os.ReadFile(path)
		if err != nil {
			return Rubric{}, err
		}
		raw = content
	}

	var r Rubric
	if err := toml.Unmarshal(raw, &r); err != nil {
		return Rubric{}, fmt.Errorf("rubric %s: %w", describe(path), err)
	}
	for name, axis := range map[string]Axis{"accuracy": r.Accuracy, "usefulness": r.Usefulness} {
		if len(axis.Criteria) != Levels {
			return Rubric{}, fmt.Errorf("rubric %s: %s has %d levels, want %d",
				describe(path), name, len(axis.Criteria), Levels)
		}
	}
	for name, axis := range map[string]Axis{"overreach": r.Overreach, "commented_out": r.CommentedOut} {
		if strings.TrimSpace(axis.Instructions) == "" {
			return Rubric{}, fmt.Errorf("rubric %s: %s.instructions is empty", describe(path), name)
		}
	}
	return r, nil
}

func describe(path string) string {
	if path == "" || path == "default" {
		return "(default)"
	}
	return path
}

type Assessor interface {
	Assess(ctx context.Context, findings []core.Finding) ([]core.Verdict, error)
	Requests() int
}

// State assembles what the model sees: context with the paired code marked,
// then the comment, each after a fixed delimiter so comment text can never
// reach the instructions.
func State(f core.Finding) string {
	var b strings.Builder
	b.WriteString("--- CONTEXT ---\n")
	if f.Context != "" {
		b.WriteString(strings.TrimRight(f.Context, "\n"))
	} else {
		b.WriteString(">>> CODE\n")
		b.WriteString(strings.TrimRight(f.CodeText, "\n"))
		b.WriteString("\n<<< CODE")
	}
	b.WriteString("\n--- COMMENT ---\n")
	b.WriteString(strings.TrimRight(cmp.Or(f.BlockText, f.CommentText), "\n"))
	b.WriteString("\n")
	return b.String()
}
