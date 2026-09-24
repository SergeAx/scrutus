package report

import (
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/SergeAx/scrutus/internal/core"
)

type Text struct{ Color bool }

const barWidth = 10

func (t Text) Report(w io.Writer, results []core.Result, run core.RunInfo) error {
	reportable := Reportable(results)
	for _, r := range reportable {
		f, v := r.Finding, r.Verdict
		fmt.Fprintf(w, "%s:%d:%d: %s %s [%s]\n",
			f.File, f.Line, f.Column,
			t.paint(severityColor(r.Severity), strings.ToUpper(string(r.Severity))),
			action(r.Action), r.Rule)
		if r.Rule == core.RuleCommentedOutCode {
			pct := int(math.Round(100 * v.CommentedOut.Prob))
			fmt.Fprintf(w, "    disabled   %s %3d%%  probability the comment is commented-out code\n", bar(pct), pct)
		} else {
			fmt.Fprintf(w, "    accuracy   %s %3d%%  %s\n", bar(v.Accuracy.Pct), v.Accuracy.Pct, v.Accuracy.Label)
			fmt.Fprintf(w, "    usefulness %s %3d%%  %s\n", bar(v.Usefulness.Pct), v.Usefulness.Pct, v.Usefulness.Label)
		}
		fmt.Fprintf(w, "    comment    %s\n", snippet(f.CommentText, 100))
		fmt.Fprintf(w, "    code       %s\n\n", snippet(f.CodeText, 100))
	}

	counts := Summary(results)
	fmt.Fprintf(w, "%d comments in %d files: %d error, %d warning, %d info",
		run.Comments, run.Files, counts["error"], counts["warning"], counts["info"])
	if run.Deleted > 0 {
		fmt.Fprintf(w, ", %d deleted", run.Deleted)
	}
	fmt.Fprintf(w, "\n%d cached, %d assessed in %d request(s), %d suppressed, %d baselined\n",
		run.Cached, run.Assessed, run.Requests, run.Suppressed, run.Baselined)

	if run.UsageUnknown {
		fmt.Fprintf(w, "cost unknown: the API reported no token usage\n")
	} else {
		fmt.Fprintf(w, "cost $%.5f (%d input tokens), %dms\n", run.CostUSD, run.InputTokens, run.DurationMS)
	}
	if run.Incomplete {
		fmt.Fprintf(w, "results are incomplete: the run stopped early\n")
	}
	return nil
}

func bar(pct int) string {
	filled := pct * barWidth / 100
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled) + "]"
}

func action(a core.Action) string {
	if a == core.ActionDelete {
		return "delete"
	}
	return "flag"
}

func severityColor(s core.Severity) string {
	switch s {
	case core.SeverityError:
		return "\x1b[31m"
	case core.SeverityWarning:
		return "\x1b[33m"
	case core.SeverityInfo:
		return "\x1b[36m"
	}
	return ""
}

func (t Text) paint(color, text string) string {
	if !t.Color || color == "" {
		return text
	}
	return color + text + "\x1b[0m"
}
