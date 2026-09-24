package classify_test

import (
	"testing"

	"github.com/SergeAx/scrutus/internal/classify"
	"github.com/SergeAx/scrutus/internal/config"
	"github.com/SergeAx/scrutus/internal/core"
)

func resolved(t *testing.T, mutate func(*config.Config)) config.Resolved {
	t.Helper()

	cfg := config.Defaults()
	if mutate != nil {
		mutate(&cfg)
	}
	r, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return r
}

func verdict(accuracy, usefulness int, confidence, overreach float64) core.Verdict {
	return core.Verdict{
		Accuracy:   core.Axis{Pct: accuracy, Confidence: confidence},
		Usefulness: core.Axis{Pct: usefulness, Confidence: confidence},
		Overreach:  core.Noul{Prob: overreach},
	}
}

func TestDefaultPolicy(t *testing.T) {
	cfg := resolved(t, nil)

	cases := []struct {
		name     string
		finding  core.Finding
		verdict  core.Verdict
		action   core.Action
		severity core.Severity
		rule     string
	}{
		{
			name:    "low confidence wins over everything",
			verdict: verdict(5, 5, 0.3, 0.99),
			action:  core.ActionFlag, severity: core.SeverityInfo, rule: core.RuleLowConfidence,
		},
		{
			name:    "overreach guard runs before the axes",
			finding: core.Finding{Kind: core.KindInline},
			verdict: verdict(10, 10, 0.95, 0.85),
			action:  core.ActionFlag, severity: core.SeverityInfo, rule: core.RuleWideScope,
		},
		{
			name:    "a doc comment is allowed to describe its whole declaration",
			finding: core.Finding{Kind: core.KindDoc},
			verdict: verdict(10, 90, 0.95, 0.95),
			action:  core.ActionFlag, severity: core.SeverityError, rule: core.RuleWrongComment,
		},
		{
			name:    "a wrong comment is an error and is never deleted",
			verdict: verdict(20, 5, 0.95, 0.1),
			action:  core.ActionFlag, severity: core.SeverityError, rule: core.RuleWrongComment,
		},
		{
			name:    "a useless comment is deleted",
			verdict: verdict(90, 10, 0.95, 0.1),
			action:  core.ActionDelete, severity: core.SeverityWarning, rule: core.RuleUselessComment,
		},
		{
			name:    "a weak axis is a warning",
			verdict: verdict(55, 80, 0.95, 0.1),
			action:  core.ActionFlag, severity: core.SeverityWarning, rule: core.RuleWeakComment,
		},
		{
			name:    "a good comment is kept",
			verdict: verdict(95, 90, 0.95, 0.05),
			action:  core.ActionKeep, severity: core.SeverityNone, rule: "",
		},
		{
			name:    "a protected doc comment is reported, not deleted",
			finding: core.Finding{Kind: core.KindDoc, Protected: true},
			verdict: verdict(90, 5, 0.95, 0.1),
			action:  core.ActionFlag, severity: core.SeverityWarning, rule: core.RuleWeakComment,
		},
		{
			name:    "a redundant annotation is reported, not deleted",
			finding: core.Finding{Kind: core.KindAnnotation},
			verdict: verdict(90, 5, 0.95, 0.1),
			action:  core.ActionFlag, severity: core.SeverityWarning, rule: core.RuleRedundantAnnotation,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify.Classify(tc.finding, tc.verdict, cfg)
			if got.Action != tc.action || got.Severity != tc.severity || got.Rule != tc.rule {
				t.Errorf("got %s/%s/%s, want %s/%s/%s",
					got.Action, got.Severity, got.Rule, tc.action, tc.severity, tc.rule)
			}
		})
	}
}

func TestDeleteAnnotationsOptIn(t *testing.T) {
	cfg := resolved(t, func(c *config.Config) { c.Comments.DeleteAnnotations = true })

	got := classify.Classify(core.Finding{Kind: core.KindAnnotation}, verdict(90, 5, 0.95, 0.1), cfg)
	if got.Action != core.ActionDelete || got.Rule != core.RuleRedundantAnnotation {
		t.Errorf("got %s/%s, want delete/%s", got.Action, got.Rule, core.RuleRedundantAnnotation)
	}
}

func TestProfilesShiftTheVerdict(t *testing.T) {
	v := verdict(50, 30, 0.95, 0.1)

	lenient := classify.Classify(core.Finding{}, v, resolved(t, func(c *config.Config) { c.Profile = "lenient" }))
	if lenient.Action != core.ActionKeep {
		t.Errorf("lenient: %s/%s, want keep", lenient.Action, lenient.Rule)
	}

	strict := classify.Classify(core.Finding{}, v, resolved(t, func(c *config.Config) { c.Profile = "strict" }))
	if strict.Rule != core.RuleWeakComment {
		t.Errorf("strict: %s/%s, want %s", strict.Action, strict.Rule, core.RuleWeakComment)
	}

	// Usefulness 20 sits above the default delete cutoff of 15 and below
	// strict's 25, so only strict deletes it.
	borderline := verdict(90, 20, 0.95, 0.1)
	if got := classify.Classify(core.Finding{}, borderline, resolved(t, nil)); got.Rule != core.RuleWeakComment {
		t.Errorf("default: %s/%s, want %s", got.Action, got.Rule, core.RuleWeakComment)
	}
	strictDelete := classify.Classify(core.Finding{}, borderline, resolved(t, func(c *config.Config) { c.Profile = "strict" }))
	if strictDelete.Action != core.ActionDelete || strictDelete.Rule != core.RuleUselessComment {
		t.Errorf("strict: %s/%s, want delete/%s", strictDelete.Action, strictDelete.Rule, core.RuleUselessComment)
	}
}

func TestEachLanguageAnswersToItsOwnKeepSetting(t *testing.T) {
	useless := verdict(90, 5, 0.95, 0.1)
	off := false
	cases := []struct {
		name    string
		finding core.Finding
		mutate  func(*config.Config)
		rule    string
	}{
		{"docstrings off deletes a docstring", core.Finding{File: "m.py", Protected: true},
			func(c *config.Config) { c.Comments.KeepDocstrings = &off }, core.RuleUselessComment},
		{"exported docs off leaves docstrings alone", core.Finding{File: "m.py", Protected: true},
			func(c *config.Config) { c.Comments.KeepExportedDocs = &off }, core.RuleWeakComment},
		{"docstrings off leaves Go docs alone", core.Finding{File: "m.go", Protected: true},
			func(c *config.Config) { c.Comments.KeepDocstrings = &off }, core.RuleWeakComment},
		{"a required docstring survives any setting", core.Finding{File: "m.py", Protected: true, Required: true},
			func(c *config.Config) { c.Comments.KeepDocstrings = &off }, core.RuleWeakComment},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify.Classify(tc.finding, useless, resolved(t, tc.mutate))
			if got.Rule != tc.rule {
				t.Errorf("got %s/%s, want %s", got.Action, got.Rule, tc.rule)
			}
		})
	}
}

func TestCommentedOutCode(t *testing.T) {
	disabled := func(prob float64) core.Verdict {
		v := verdict(5, 5, 0.3, 0.1)
		v.CommentedOut.Prob = prob
		return v
	}
	strict := func(c *config.Config) { c.Profile = "strict" }
	cases := []struct {
		name     string
		finding  core.Finding
		verdict  core.Verdict
		mutate   func(*config.Config)
		action   core.Action
		severity core.Severity
		rule     string
	}{
		{"ignored by default, whatever the axes say", core.Finding{}, disabled(0.9), nil,
			core.ActionKeep, core.SeverityNone, ""},
		{"deleted under strict", core.Finding{}, disabled(0.9), strict,
			core.ActionDelete, core.SeverityWarning, core.RuleCommentedOutCode},
		{"deleted when the config asks, under any profile", core.Finding{}, disabled(0.9),
			func(c *config.Config) { c.Comments.CommentedOutCode = "delete" },
			core.ActionDelete, core.SeverityWarning, core.RuleCommentedOutCode},
		{"kept when the config asks, under strict", core.Finding{}, disabled(0.9),
			func(c *config.Config) { c.Profile = "strict"; c.Comments.CommentedOutCode = "ignore" },
			core.ActionKeep, core.SeverityNone, ""},
		{"a protected doc comment is reported, not deleted", core.Finding{Kind: core.KindDoc, Protected: true}, disabled(0.9), strict,
			core.ActionFlag, core.SeverityWarning, core.RuleCommentedOutCode},
		{"below the guard the axes decide", core.Finding{}, disabled(0.6), strict,
			core.ActionFlag, core.SeverityInfo, core.RuleLowConfidence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify.Classify(tc.finding, tc.verdict, resolved(t, tc.mutate))
			if got.Action != tc.action || got.Severity != tc.severity || got.Rule != tc.rule {
				t.Errorf("got %s/%s/%s, want %s/%s/%s",
					got.Action, got.Severity, got.Rule, tc.action, tc.severity, tc.rule)
			}
		})
	}
}
