package scrutus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	typesafe "serge.ax/go/typesafe-sdk-go"

	"github.com/SergeAx/scrutus/internal/assess"
	"github.com/SergeAx/scrutus/internal/assess/jev"
	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/jevstub"
	"github.com/SergeAx/scrutus/pkg/scrutus"
)

func stubAssessor(t *testing.T) (assess.Assessor, *jevstub.Server) {
	t.Helper()
	return stubAssessorFrom(t, "payments.json")
}

func stubAssessorFrom(t *testing.T, fixture string) (assess.Assessor, *jevstub.Server) {
	t.Helper()
	return stubAssessorWith(t, loadFixtures(t, fixture), 0)
}

func loadFixtures(t *testing.T, fixture string) jevstub.Fixtures {
	t.Helper()

	fixtures, err := jevstub.Load(filepath.Join("..", "..", "testdata", "jev", fixture))
	if err != nil {
		t.Fatalf("fixtures: %v", err)
	}
	return fixtures
}

func stubAssessorWith(t *testing.T, fixtures jevstub.Fixtures, budgetUSD float64) (assess.Assessor, *jevstub.Server) {
	t.Helper()

	server := jevstub.New(fixtures)
	t.Cleanup(server.Close)

	client, err := typesafe.New(
		typesafe.WithAPIKey("test-key"),
		typesafe.WithBaseURL(server.URL),
		typesafe.WithLogger(discardLogger()),
	)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	rubric, err := assess.LoadRubric("default")
	if err != nil {
		t.Fatalf("rubric: %v", err)
	}
	return jev.New(client, rubric, 4, budgetUSD), server
}

// copySample clones the sample corpus into a scratch directory, so fix can
// edit it without touching the fixtures.
func copySample(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	source := filepath.Join("..", "..", "testdata", "sample")
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatalf("read sample: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name()), content, 0o644); err != nil {
			t.Fatalf("write sample: %v", err)
		}
	}
	return dir
}

func samplePath(t *testing.T, dir string) string {
	t.Helper()
	return filepath.Join(dir, "payments.go")
}

func checkSample(t *testing.T, format string, extra func(*scrutus.Options)) (scrutus.Report, string) {
	t.Helper()

	assessor, _ := stubAssessor(t)
	var out bytes.Buffer
	opts := scrutus.Options{
		Mode:     scrutus.ModeCheck,
		Paths:    []string{copySample(t)},
		Format:   format,
		NoCache:  true,
		NoDotenv: true,
		Assessor: assessor,
		Out:      &out,
	}
	if extra != nil {
		extra(&opts)
	}
	report, err := scrutus.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return report, out.String()
}

func TestCheckClassifiesSampleFile(t *testing.T) {
	report, _ := checkSample(t, "json", nil)

	want := map[string]string{
		"multiplies the amount by three":             core.RuleWrongComment,
		"Collapse internal whitespace and lowercase": core.RuleWideScope,
		"Build the reference":                        core.RuleUselessComment,
		"Increment the attempt counter":              core.RuleUselessComment,
		"delay in milliseconds":                      core.RuleWeakComment,
		"CreatedAt is the creation timestamp":        core.RuleWeakComment,
		"ErrRefunded is returned":                    core.RuleLowConfidence,
	}
	for fragment, rule := range want {
		result, ok := find(report.Results, fragment)
		if !ok {
			t.Errorf("no finding for %q", fragment)
			continue
		}
		if result.Rule != rule {
			t.Errorf("%q: rule %s, want %s", fragment, result.Rule, rule)
		}
	}

	for _, fragment := range []string{
		"Stripe rejects references longer than 40 bytes",
		"AmountCents is the amount in minor units",
		"strings.TrimPrefix",
	} {
		if result, ok := find(report.Results, fragment); ok && result.Action != core.ActionKeep {
			t.Errorf("%q: %s, want keep", fragment, result.Rule)
		}
	}
}

func TestExemptAndSuppressedCommentsAreNeverScored(t *testing.T) {
	report, _ := checkSample(t, "json", nil)

	for _, fragment := range []string{
		"TODO: support partial refunds",
		"Returns the ledger prefix",
		"Returns the currency",
	} {
		if _, ok := find(report.Results, fragment); ok {
			t.Errorf("%q reached the assessor", fragment)
		}
	}
	if report.Run.Suppressed != 2 {
		t.Errorf("suppressed = %d, want 2", report.Run.Suppressed)
	}
}

func TestExportedDocCommentIsReportedButNeverDeleted(t *testing.T) {
	report, _ := checkSample(t, "json", nil)

	result, ok := find(report.Results, "CreatedAt is the creation timestamp")
	if !ok {
		t.Fatal("no finding for the exported field doc comment")
	}
	if result.Action != core.ActionFlag || result.Rule != core.RuleWeakComment {
		t.Errorf("exported doc comment: %s/%s, want flag/%s",
			result.Action, result.Rule, core.RuleWeakComment)
	}

	inline, ok := find(report.Results, "Build the reference")
	if !ok {
		t.Fatal("no finding for the inline comment")
	}
	if inline.Action != core.ActionDelete {
		t.Errorf("inline useless comment: action %s, want delete", inline.Action)
	}
}

func TestProfilesMoveTheThresholds(t *testing.T) {
	lenient, _ := checkSample(t, "json", func(o *scrutus.Options) { o.Profile = "lenient" })
	strict, _ := checkSample(t, "json", func(o *scrutus.Options) { o.Profile = "strict" })

	if len(reportable(strict)) <= len(reportable(lenient)) {
		t.Errorf("strict reported %d findings, lenient %d; strict should report more",
			len(reportable(strict)), len(reportable(lenient)))
	}
}

func TestJSONReportIsSchemaShaped(t *testing.T) {
	_, output := checkSample(t, "json", nil)

	var parsed struct {
		SchemaVersion int `json:"schema_version"`
		Tool          struct {
			Name          string `json:"name"`
			RubricVersion int    `json:"rubric_version"`
		} `json:"tool"`
		Backend struct {
			Model string `json:"model"`
		} `json:"backend"`
		Run struct {
			Comments int `json:"comments"`
			Assessed int `json:"assessed"`
			Requests int `json:"requests"`
		} `json:"run"`
		Results []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
			Span     struct {
				Start int `json:"start"`
				End   int `json:"end"`
			} `json:"span"`
		} `json:"results"`
		Summary map[string]int `json:"summary"`
	}
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("json: %v\n%s", err, output)
	}
	rubric, err := assess.LoadRubric("default")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SchemaVersion != 2 || parsed.Tool.Name != "scrutus" || parsed.Tool.RubricVersion != rubric.Version {
		t.Errorf("tool block: %+v", parsed.Tool)
	}
	if parsed.Backend.Model != "jev-1.13.0" {
		t.Errorf("model = %q, want the model the response reported", parsed.Backend.Model)
	}
	if parsed.Run.Requests == 0 || parsed.Run.Requests > parsed.Run.Assessed {
		t.Errorf("requests = %d, assessed = %d", parsed.Run.Requests, parsed.Run.Assessed)
	}
	if len(parsed.Results) == 0 {
		t.Fatal("no results")
	}
	for _, r := range parsed.Results {
		if r.Span.End <= r.Span.Start {
			t.Errorf("%s: empty span %+v", r.Rule, r.Span)
		}
	}
}

func TestSARIFCarriesDeletionFixes(t *testing.T) {
	_, output := checkSample(t, "sarif", nil)

	var parsed struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []struct {
				RuleID string `json:"ruleId"`
				Level  string `json:"level"`
				Fixes  []struct {
					Changes []struct {
						Replacements []struct {
							DeletedRegion struct {
								ByteLength int `json:"byteLength"`
							} `json:"deletedRegion"`
						} `json:"replacements"`
					} `json:"artifactChanges"`
				} `json:"fixes"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("sarif: %v\n%s", err, output)
	}
	if parsed.Version != "2.1.0" || len(parsed.Runs) != 1 {
		t.Fatalf("sarif shape: version %q, %d runs", parsed.Version, len(parsed.Runs))
	}

	fixes := 0
	for _, r := range parsed.Runs[0].Results {
		if r.RuleID == core.RuleUselessComment {
			if len(r.Fixes) == 0 {
				t.Errorf("%s carries no fix", r.RuleID)
				continue
			}
			if got := r.Fixes[0].Changes[0].Replacements[0].DeletedRegion.ByteLength; got == 0 {
				t.Errorf("%s: deleted region is empty", r.RuleID)
			}
			fixes++
		}
		if r.RuleID == core.RuleWrongComment && len(r.Fixes) != 0 {
			t.Error("a wrong comment must never carry a deletion fix")
		}
	}
	if fixes == 0 {
		t.Error("no deletable findings reached SARIF")
	}
}

func TestFixDeletesOnlyUselessComments(t *testing.T) {
	outsideCI(t)
	assessor, _ := stubAssessor(t)
	dir := copySample(t)
	path := samplePath(t, dir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	report, err := scrutus.Run(context.Background(), scrutus.Options{
		Mode:     scrutus.ModeFix,
		Paths:    []string{dir},
		Format:   "json",
		NoCache:  true,
		NoDotenv: true,
		Assessor: assessor,
		Out:      &out,
	})
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	if report.Run.Deleted == 0 {
		t.Fatal("nothing was deleted")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(after)
	for _, gone := range []string{"// Build the reference.", "// Increment the attempt counter."} {
		if strings.Contains(text, gone) {
			t.Errorf("%q survived fix", gone)
		}
	}
	for _, kept := range []string{
		"// Total multiplies the amount by three.",
		"// Stripe rejects references longer than 40 bytes",
		"//scrutus:ignore",
		"// TODO: support partial refunds",
		"// reference = strings.TrimPrefix",
	} {
		if !strings.Contains(text, kept) {
			t.Errorf("%q was deleted", kept)
		}
	}
	if len(after) >= len(before) {
		t.Errorf("file did not shrink: %d -> %d bytes", len(before), len(after))
	}
	if !parses(t, after) {
		t.Error("the edited file no longer parses")
	}
}

func TestStrictFixDeletesCommentedOutCode(t *testing.T) {
	outsideCI(t)
	assessor, _ := stubAssessor(t)
	dir := copySample(t)

	var out bytes.Buffer
	report, err := scrutus.Run(context.Background(), scrutus.Options{
		Mode:     scrutus.ModeFix,
		Paths:    []string{dir},
		Profile:  "strict",
		Format:   "text",
		NoCache:  true,
		NoDotenv: true,
		Assessor: assessor,
		Out:      &out,
	})
	if err != nil {
		t.Fatalf("fix: %v", err)
	}

	result, ok := find(report.Results, "strings.TrimPrefix")
	if !ok || result.Action != core.ActionDelete || result.Rule != core.RuleCommentedOutCode {
		t.Fatalf("commented-out code: %+v, want delete/%s", result, core.RuleCommentedOutCode)
	}
	if !strings.Contains(out.String(), "93%  probability the comment is commented-out code") {
		t.Errorf("text report shows no probability:\n%s", out.String())
	}
	after, err := os.ReadFile(samplePath(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "strings.TrimPrefix") {
		t.Error("the commented-out code survived fix")
	}
	if !parses(t, after) {
		t.Error("the edited file no longer parses")
	}
}

func TestFixDiffWritesNothing(t *testing.T) {
	assessor, _ := stubAssessor(t)
	dir := copySample(t)
	path := samplePath(t, dir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := scrutus.Run(context.Background(), scrutus.Options{
		Mode:     scrutus.ModeFix,
		Paths:    []string{dir},
		Format:   "json",
		Diff:     true,
		NoCache:  true,
		NoDotenv: true,
		Assessor: assessor,
		Out:      &out,
	}); err != nil {
		t.Fatalf("fix --diff: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("--diff modified the file")
	}
	if !strings.Contains(out.String(), "-\t// Build the reference.") {
		t.Errorf("diff does not show the deletion:\n%s", out.String())
	}
}

func TestSecondRunIsFullyCached(t *testing.T) {
	assessor, server := stubAssessor(t)
	dir := copySample(t)
	cacheDir := t.TempDir()
	t.Setenv("SCRUTUS_CACHE_DIR", cacheDir)

	run := func() scrutus.Report {
		var out bytes.Buffer
		report, err := scrutus.Run(context.Background(), scrutus.Options{
			Mode:     scrutus.ModeCheck,
			Paths:    []string{dir},
			Format:   "json",
			NoDotenv: true,
			Assessor: assessor,
			Out:      &out,
		})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		return report
	}

	first := run()
	if first.Run.Assessed == 0 {
		t.Fatal("first run assessed nothing")
	}
	requestsAfterFirst := server.Requests()

	second := run()
	if second.Run.Assessed != 0 {
		t.Errorf("second run assessed %d findings, want 0", second.Run.Assessed)
	}
	if second.Run.Cached != first.Run.Assessed {
		t.Errorf("second run cached %d, want %d", second.Run.Cached, first.Run.Assessed)
	}
	if server.Requests() != requestsAfterFirst {
		t.Errorf("second run made %d extra requests", server.Requests()-requestsAfterFirst)
	}
	if second.Run.CostUSD != 0 {
		t.Errorf("cached run cost $%f, want 0", second.Run.CostUSD)
	}
}

func TestBudgetRefusesBeforeTheFirstRequest(t *testing.T) {
	assessor, server := stubAssessor(t)
	var out bytes.Buffer
	_, err := scrutus.Run(context.Background(), scrutus.Options{
		Mode:        scrutus.ModeCheck,
		Paths:       []string{copySample(t)},
		Format:      "json",
		BudgetCents: 0.0001,
		NoCache:     true,
		NoDotenv:    true,
		Assessor:    assessor,
		Out:         &out,
	})
	if err == nil {
		t.Fatal("the run did not refuse")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("error = %v, want a budget refusal", err)
	}
	if server.Requests() != 0 {
		t.Errorf("%d requests were made despite the budget", server.Requests())
	}
}

func TestBudgetStopReportsWhatWasScored(t *testing.T) {
	fixtures := loadFixtures(t, "payments.json")
	fixtures.InputTokens = 100_000
	assessor, _ := stubAssessorWith(t, fixtures, 0.005)
	var out bytes.Buffer
	report, err := scrutus.Run(context.Background(), scrutus.Options{
		Mode:     scrutus.ModeCheck,
		Paths:    []string{copySample(t)},
		Format:   "json",
		NoCache:  true,
		NoDotenv: true,
		Assessor: assessor,
		Out:      &out,
	})

	if !errors.Is(err, scrutus.ErrBudget) {
		t.Fatalf("err = %v, want a budget stop", err)
	}
	if !report.Run.Incomplete || len(report.Results) == 0 {
		t.Errorf("incomplete = %v with %d results, want the scored ones marked incomplete",
			report.Run.Incomplete, len(report.Results))
	}
	if !strings.Contains(out.String(), `"incomplete"`) {
		t.Errorf("the report does not say it is incomplete:\n%s", out.String())
	}
}

func TestFixRefusesToWriteInCI(t *testing.T) {
	assessor, _ := stubAssessor(t)
	t.Setenv("CI", "true")

	var out bytes.Buffer
	_, err := scrutus.Run(context.Background(), scrutus.Options{
		Mode:     scrutus.ModeFix,
		Paths:    []string{copySample(t)},
		NoCache:  true,
		NoDotenv: true,
		Assessor: assessor,
		Out:      &out,
	})
	if err == nil {
		t.Fatal("fix wrote files in CI")
	}
	if !strings.Contains(err.Error(), "allow-ci-write") {
		t.Errorf("error = %v, want the CI refusal", err)
	}
}

func TestBaselineSilencesThenSurfacesAgain(t *testing.T) {
	assessor, _ := stubAssessor(t)
	dir := copySample(t)
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")

	var out bytes.Buffer
	options := scrutus.Options{
		Mode:         scrutus.ModeBaseline,
		Paths:        []string{dir},
		Format:       "json",
		BaselinePath: baselinePath,
		NoCache:      true,
		NoDotenv:     true,
		Assessor:     assessor,
		Out:          &out,
	}
	if _, err := scrutus.Run(context.Background(), options); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	options.Mode = scrutus.ModeCheck
	report, err := scrutus.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(reportable(report)) != 0 {
		t.Errorf("%d findings survived the baseline", len(reportable(report)))
	}
	if report.Run.Baselined == 0 {
		t.Error("baselined findings were not counted")
	}
}

func TestEveryDocblockTagIsGradedOnItsOwnLines(t *testing.T) {
	dir := t.TempDir()
	const ledger = `<?php

class Ledger
{
    /**
     * Settles the invoice against the ledger.
     *
     * @param int $id
     * @return string the reference in lowercase
     */
    public function settle(int $id): string
    {
        return strtoupper((string) $id);
    }
}
`
	if err := os.WriteFile(filepath.Join(dir, "Ledger.php"), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	assessor, server := stubAssessorFrom(t, "docblock.json")
	report, err := scrutus.Run(context.Background(), scrutus.Options{
		Mode:     scrutus.ModeCheck,
		Paths:    []string{dir},
		Format:   "json",
		NoCache:  true,
		NoDotenv: true,
		Assessor: assessor,
		Out:      &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	want := map[string]string{
		"Settles the invoice":                       "",
		"@param int $id":                            core.RuleRedundantAnnotation,
		"@return string the reference in lowercase": core.RuleWrongComment,
	}
	for fragment, rule := range want {
		result, ok := find(report.Results, fragment)
		if !ok {
			t.Errorf("%q was not scored", fragment)
			continue
		}
		if result.Rule != rule {
			t.Errorf("%q: rule %q, want %q", fragment, result.Rule, rule)
		}
	}
	if server.Requests() != 1 {
		t.Errorf("%d requests, want the docblock's one", server.Requests())
	}
}

func find(results []core.Result, fragment string) (core.Result, bool) {
	for _, r := range results {
		if strings.Contains(r.Finding.CommentText, fragment) {
			return r, true
		}
	}
	return core.Result{}, false
}

func reportable(report scrutus.Report) []core.Result {
	var out []core.Result
	for _, r := range report.Results {
		if r.Action != core.ActionKeep {
			out = append(out, r)
		}
	}
	return out
}
