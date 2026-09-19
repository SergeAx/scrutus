// Package classify turns two percentages into an action. The model never
// chooses; every threshold lives here.
package classify

import (
	"github.com/SergeAx/scrutus/internal/config"
	"github.com/SergeAx/scrutus/internal/core"
)

// OverreachGuard is the probability above which a comment is treated as
// scoped wider than its paired code rather than as wrong about it.
const OverreachGuard = 0.8

func Classify(f core.Finding, v core.Verdict, cfg config.Resolved) core.Result {
	r := core.Result{Finding: f, Verdict: v, Action: core.ActionKeep}

	if deciding(v) < cfg.MinConfidence {
		return flag(r, core.SeverityInfo, core.RuleLowConfidence)
	}
	// A doc comment describes the declaration it is paired with, so breadth is
	// its job, not a finding.
	if f.Kind != core.KindDoc && v.Overreach.Prob >= OverreachGuard {
		return flag(r, core.SeverityInfo, core.RuleWideScope)
	}
	// Accuracy before Usefulness: a wrong comment is an error even when it is
	// also useless, and it is never auto-deleted.
	if v.Accuracy.Pct <= cfg.AccuracyError {
		return flag(r, core.SeverityError, core.RuleWrongComment)
	}
	if v.Usefulness.Pct <= cfg.UsefulnessDelete {
		return useless(r, f, cfg)
	}
	if v.Accuracy.Pct <= cfg.AccuracyWarning || v.Usefulness.Pct <= cfg.UsefulnessWarn {
		return flag(r, core.SeverityWarning, core.RuleWeakComment)
	}
	return r
}

func useless(r core.Result, f core.Finding, cfg config.Resolved) core.Result {
	if f.Kind == core.KindAnnotation {
		r.Rule = core.RuleRedundantAnnotation
		r.Severity = core.SeverityWarning
		r.Action = core.ActionFlag
		if cfg.Comments.DeleteAnnotations {
			r.Action = core.ActionDelete
		}
		return r
	}
	// An exported Go doc comment or a Python docstring is load-bearing: report
	// it, never delete it.
	if f.Protected && (cfg.Comments.KeepExported() || cfg.Comments.KeepDocstring()) {
		return flag(r, core.SeverityWarning, core.RuleWeakComment)
	}
	r.Action = core.ActionDelete
	r.Severity = core.SeverityWarning
	r.Rule = core.RuleUselessComment
	return r
}

func flag(r core.Result, severity core.Severity, rule string) core.Result {
	r.Action = core.ActionFlag
	r.Severity = severity
	r.Rule = rule
	return r
}

// deciding is the confidence the decision rests on: the weaker axis, since
// that is the one a threshold is most likely to catch.
func deciding(v core.Verdict) float64 {
	if v.Accuracy.Pct <= v.Usefulness.Pct {
		return v.Accuracy.Confidence
	}
	return v.Usefulness.Confidence
}
