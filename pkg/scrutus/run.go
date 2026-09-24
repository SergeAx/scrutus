// Package scrutus is the embeddable API: one Run over one Options, with the
// pipeline of the design spec behind it.
package scrutus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	typesafe "serge.ax/go/typesafe-sdk-go"

	"github.com/SergeAx/scrutus/internal/assess"
	"github.com/SergeAx/scrutus/internal/assess/jev"
	"github.com/SergeAx/scrutus/internal/baseline"
	"github.com/SergeAx/scrutus/internal/cache"
	"github.com/SergeAx/scrutus/internal/classify"
	"github.com/SergeAx/scrutus/internal/config"
	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
	_ "github.com/SergeAx/scrutus/internal/extract/golang"
	_ "github.com/SergeAx/scrutus/internal/extract/php"
	_ "github.com/SergeAx/scrutus/internal/extract/treesitter"
	"github.com/SergeAx/scrutus/internal/filter"
	"github.com/SergeAx/scrutus/internal/fix"
	"github.com/SergeAx/scrutus/internal/report"
	"github.com/SergeAx/scrutus/internal/scope"
)

const Version = "0.1.0"

type Mode string

const (
	ModeCheck    Mode = "check"
	ModeFix      Mode = "fix"
	ModeBaseline Mode = "baseline"
)

type Options struct {
	Mode    Mode
	Paths   []string
	Staged  bool
	Changed bool
	Base    string

	ConfigPath      string
	Profile         string
	BaselinePath    string
	Format          string
	MinConfidence   float64
	BudgetCents     float64
	Concurrency     int
	DeleteThreshold int
	NoCache         bool
	NoDotenv        bool
	SoftFail        bool
	Diff            bool
	DryRun          bool
	AllowCIWrite    bool
	Color           bool

	Out    io.Writer
	Logger *slog.Logger

	// Assessor replaces the Jev backend, which is how tests run without a
	// network and how a fully cached run avoids needing a key.
	Assessor assess.Assessor
}

type Report struct {
	Results []core.Result
	Run     core.RunInfo
	Fixes   []fix.FileResult
	Worst   core.Severity
}

var (
	ErrBudget    = jev.ErrBudget
	ErrNoAPIKey  = errors.New("no API key")
	ErrCIWrite   = errors.New("fix refuses to write in CI")
	ErrSoftFiled = errors.New("soft-failed")
)

// Run executes the pipeline. A returned error is a tool error; findings are
// reported through Report.
func Run(ctx context.Context, opts Options) (Report, error) {
	started := time.Now()
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Mode == "" {
		opts.Mode = ModeCheck
	}
	if opts.Format == "" {
		opts.Format = "text"
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 8
	}

	if !opts.NoDotenv {
		config.LoadDotenv()
	}
	if opts.Mode == ModeFix && !opts.Diff && !opts.DryRun && inCI() && !opts.AllowCIWrite {
		return Report{}, fmt.Errorf("%w: use check --format sarif, or pass --allow-ci-write", ErrCIWrite)
	}

	cfg, err := loadConfig(opts)
	if err != nil {
		return Report{}, err
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		return Report{}, err
	}
	applyOverrides(&resolved, opts)

	rubric, err := assess.LoadRubric(resolved.Rubric)
	if err != nil {
		return Report{}, err
	}

	scoped, err := scope.Resolve(scope.Options{
		Paths: opts.Paths, Staged: opts.Staged, Changed: opts.Changed,
		Base: opts.Base, Ignore: resolved.Ignore,
	})
	if err != nil {
		return Report{}, err
	}

	findings, sources, files, err := collect(scoped, resolved, rubric.Version)
	if err != nil {
		return Report{}, err
	}

	var base *baseline.File
	if path := baselinePath(opts); path != "" && opts.Mode != ModeBaseline {
		if loaded, err := baseline.Load(path); err == nil {
			base = loaded
		} else if !errors.Is(err, os.ErrNotExist) {
			return Report{}, err
		}
	}

	kept, counts := filter.Apply(findings, sources, resolved.Comments, base)

	store, apiKey, err := openCache(resolved, opts)
	if err != nil {
		return Report{}, err
	}
	defer store.Close()

	run := core.RunInfo{
		Mode: string(opts.Mode), Scope: scoped.Mode, Base: scoped.Base,
		Files: files, Comments: len(findings),
		Suppressed: counts.Suppressed, Baselined: counts.Baselined,
		Tool: "scrutus", Version: Version, RubricVer: rubric.Version,
		Model: resolved.Jev.Model,
	}

	verdicts := map[string]core.Verdict{}
	var pending []core.Finding
	for _, f := range kept {
		if v, ok := store.Get(f.ID); ok {
			verdicts[f.ID] = v
			run.Cached++
			continue
		}
		pending = append(pending, f)
	}

	assessor := opts.Assessor
	if len(pending) > 0 && assessor == nil {
		assessor, err = newAssessor(resolved, rubric, apiKey, opts)
		if err != nil {
			return Report{}, err
		}
	}

	fresh := map[string]bool{}
	var stopped error
	if len(pending) > 0 {
		if err := withinBudget(pending, rubric, opts.BudgetCents); err != nil {
			return Report{}, err
		}
		assessed, err := assessor.Assess(ctx, pending)
		for _, v := range assessed {
			verdicts[v.FindingID] = v
			fresh[v.FindingID] = true
			if putErr := store.Put(v.FindingID, v); putErr != nil && err == nil {
				err = putErr
			}
		}
		run.Assessed = len(assessed)
		run.Requests = assessor.Requests()
		if err != nil {
			run.Incomplete = true
			if opts.SoftFail && transportFailure(err) {
				return Report{Run: run}, fmt.Errorf("%w: %v", ErrSoftFiled, err)
			}
			stopped = err
		}
	}

	var results []core.Result
	for _, f := range kept {
		v, ok := verdicts[f.ID]
		if !ok {
			continue
		}
		if fresh[f.ID] {
			run.InputTokens += v.Usage.InputTokens
			run.CostUSD += v.Usage.CostUSD
		}
		if v.Model != "" {
			run.Model = v.Model
		}
		results = append(results, classify.Classify(f, v, resolved))
	}
	run.UsageUnknown = run.Assessed > 0 && run.InputTokens == 0

	out := Report{Results: results, Run: run, Worst: report.Worst(results)}

	// Verdicts already paid for are reported, but nothing is fixed or
	// baselined from a run that did not score every comment.
	if stopped != nil {
		out.Run.DurationMS = time.Since(started).Milliseconds()
		return out, errors.Join(stopped, emit(opts, out))
	}

	if opts.Mode == ModeFix {
		applied, err := fix.Apply(results, sources, fix.Options{
			DryRun:    opts.Diff || opts.DryRun,
			Languages: resolved.Languages,
		})
		if err != nil {
			return out, err
		}
		out.Fixes = applied
		for _, f := range applied {
			out.Run.Deleted += f.Deleted
		}
	}

	out.Run.DurationMS = time.Since(started).Milliseconds()

	if opts.Mode == ModeBaseline {
		return out, baseline.Write(baselinePath(opts), results)
	}
	if err := emit(opts, out); err != nil {
		return out, err
	}
	return out, nil
}

func emit(opts Options, out Report) error {
	if opts.Mode == ModeFix && (opts.Diff || opts.DryRun) {
		for _, f := range out.Fixes {
			if f.Err != nil {
				fmt.Fprintf(opts.Out, "%v\n", f.Err)
				continue
			}
			if opts.Diff {
				fmt.Fprint(opts.Out, f.Diff)
			} else {
				fmt.Fprintf(opts.Out, "%s: %d comment(s) would be deleted\n", f.Path, f.Deleted)
			}
		}
	}

	reporter, err := report.For(opts.Format, opts.Color)
	if err != nil {
		return err
	}
	return reporter.Report(opts.Out, out.Results, out.Run)
}

func loadConfig(opts Options) (config.Config, error) {
	path := opts.ConfigPath
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return config.Config{}, err
		}
		found, err := config.Find(cwd)
		if err != nil {
			return config.Config{}, err
		}
		path = found
	}
	if path == "" {
		cfg := config.Defaults()
		if opts.Profile != "" {
			cfg.Profile = opts.Profile
		}
		return cfg, nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return cfg, err
	}
	if opts.Profile != "" {
		cfg.Profile = opts.Profile
	}
	return cfg, nil
}

func applyOverrides(resolved *config.Resolved, opts Options) {
	if opts.MinConfidence > 0 {
		resolved.MinConfidence = opts.MinConfidence
	}
	if opts.DeleteThreshold > 0 {
		resolved.UsefulnessDelete = opts.DeleteThreshold
	}
}

func baselinePath(opts Options) string {
	if opts.BaselinePath != "" {
		return opts.BaselinePath
	}
	if opts.Mode == ModeBaseline {
		return baseline.FileName
	}
	if _, err := os.Stat(baseline.FileName); err == nil {
		return baseline.FileName
	}
	return ""
}

// collect extracts every file in scope, keeping comments that intersect the
// scoped line ranges.
func collect(scoped scope.Result, cfg config.Resolved, rubricVersion int) ([]core.Finding, map[string][]byte, int, error) {
	sources := map[string][]byte{}
	var findings []core.Finding
	parsed := 0

	for _, file := range scoped.Files {
		src, err := os.ReadFile(file.Path)
		if err != nil {
			return nil, nil, 0, err
		}
		if isBinary(src) {
			continue
		}
		extracted, err := extract.Extract(file.Path, src, cfg.Languages, extract.Options{
			ContextLines: cfg.Comments.ContextLines,
		})
		if err != nil {
			if _, ok := errors.AsType[*extract.MissingExtractorError](err); ok {
				return nil, nil, 0, err
			}
			fmt.Fprintf(os.Stderr, "scrutus: %s: skipped: %v\n", file.Path, err)
			continue
		}
		if len(extracted) == 0 {
			continue
		}

		sources[file.Path] = src
		parsed++
		for _, f := range extracted {
			endLine := f.Line + strings.Count(f.CommentText, "\n")
			if !scope.Intersects(file.Ranges, f.Line, endLine) {
				continue
			}
			f.ID = core.NewID(f.CommentText, f.CodeText, rubricVersion, cfg.Jev.Model)
			findings = append(findings, f)
		}
	}
	return findings, sources, parsed, nil
}

func isBinary(src []byte) bool {
	limit := min(len(src), 8000)
	for i := range limit {
		if src[i] == 0 {
			return true
		}
	}
	return false
}

type cacheStore interface {
	Get(string) (core.Verdict, bool)
	Put(string, core.Verdict) error
	Close() error
}

func openCache(cfg config.Resolved, opts Options) (cacheStore, string, error) {
	apiKey := os.Getenv(apiKeyEnv(cfg))
	if opts.NoCache {
		return cache.Noop{}, apiKey, nil
	}
	store, err := cache.Open(cfg.Cache.Dir, apiKey, time.Duration(cfg.Cache.TTL))
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrutus: cache unavailable, continuing uncached: %v\n", err)
		return cache.Noop{}, apiKey, nil
	}
	return store, apiKey, nil
}

func apiKeyEnv(cfg config.Resolved) string {
	if cfg.Jev.APIKeyEnv != "" {
		return cfg.Jev.APIKeyEnv
	}
	return typesafe.APIKeyEnv
}

func newAssessor(cfg config.Resolved, rubric assess.Rubric, apiKey string, opts Options) (assess.Assessor, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("%w: set %s, or put it in one of %s",
			ErrNoAPIKey, apiKeyEnv(cfg), strings.Join(config.DotenvPaths(), ", "))
	}

	clientOptions := []typesafe.ClientOption{
		typesafe.WithAPIKey(apiKey),
		typesafe.WithDefaultModel(cfg.Jev.Model),
		typesafe.WithTimeout(time.Duration(cfg.Jev.Timeout)),
	}
	if opts.Logger != nil {
		clientOptions = append(clientOptions, typesafe.WithLogger(opts.Logger))
	}
	client, err := typesafe.New(clientOptions...)
	if err != nil {
		return nil, err
	}
	return jev.New(client, rubric, opts.Concurrency, opts.BudgetCents/100), nil
}

// withinBudget refuses before the first request.
func withinBudget(pending []core.Finding, rubric assess.Rubric, budgetCents float64) error {
	if budgetCents <= 0 {
		return nil
	}
	estimate := float64(jev.EstimateTokens(pending, rubric)) * jev.USDPerInputToken * 100
	if estimate > budgetCents {
		return fmt.Errorf("%w: estimated %.2f¢ for %d findings, budget %.2f¢",
			ErrBudget, estimate, len(pending), budgetCents)
	}
	return nil
}

func transportFailure(err error) bool {
	return errors.Is(err, typesafe.ErrConnection) || errors.Is(err, typesafe.ErrTimeout)
}

func inCI() bool {
	for _, name := range []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI"} {
		if value := os.Getenv(name); value != "" && value != "false" && value != "0" {
			return true
		}
	}
	return false
}
