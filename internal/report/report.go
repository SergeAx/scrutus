// Package report projects results into every output format. The JSON schema is
// canonical; the others are views of it.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/SergeAx/scrutus/internal/core"
)

const SchemaVersion = 1

type Reporter interface {
	Report(w io.Writer, results []core.Result, run core.RunInfo) error
}

func For(format string, color bool) (Reporter, error) {
	switch format {
	case "text":
		return Text{Color: color}, nil
	case "json":
		return JSON{}, nil
	case "sarif":
		return SARIF{}, nil
	case "github":
		return GitHub{}, nil
	case "rdjson":
		return RDJSON{}, nil
	case "checkstyle":
		return Checkstyle{}, nil
	}
	return nil, fmt.Errorf("unknown format %q, want text, json, sarif, github, rdjson or checkstyle", format)
}

// Reportable keeps the findings worth printing: everything the classifier
// acted on.
func Reportable(results []core.Result) []core.Result {
	out := make([]core.Result, 0, len(results))
	for _, r := range results {
		if r.Action != core.ActionKeep {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Finding.File != out[j].Finding.File {
			return out[i].Finding.File < out[j].Finding.File
		}
		return out[i].Finding.Line < out[j].Finding.Line
	})
	return out
}

func Summary(results []core.Result) map[string]int {
	counts := map[string]int{"error": 0, "warning": 0, "info": 0, "deleted": 0}
	for _, r := range results {
		if r.Severity != core.SeverityNone {
			counts[string(r.Severity)]++
		}
		if r.Action == core.ActionDelete {
			counts["deleted"]++
		}
	}
	return counts
}

// Worst is the highest severity present, which --fail-on is compared against.
func Worst(results []core.Result) core.Severity {
	worst := core.SeverityNone
	for _, r := range results {
		if core.SeverityRank(r.Severity) > core.SeverityRank(worst) {
			worst = r.Severity
		}
	}
	return worst
}

func snippet(text string, limit int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	joined := strings.Join(lines, " ")
	if len([]rune(joined)) <= limit {
		return joined
	}
	return string([]rune(joined)[:limit-1]) + "…"
}
