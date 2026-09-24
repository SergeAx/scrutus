// Command scrutus grades source comments for accuracy and usefulness.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"
	typesafe "serge.ax/go/typesafe-sdk-go"

	"github.com/SergeAx/scrutus/internal/assess"
	"github.com/SergeAx/scrutus/internal/cache"
	"github.com/SergeAx/scrutus/internal/config"
	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
	_ "github.com/SergeAx/scrutus/internal/extract/golang"
	_ "github.com/SergeAx/scrutus/internal/extract/php"
	_ "github.com/SergeAx/scrutus/internal/extract/treesitter"
	"github.com/SergeAx/scrutus/pkg/scrutus"
)

const (
	exitFindings  = 1
	exitToolError = 2
	exitBudget    = 3
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		opts       scrutus.Options
		outputPath string
		failOn     string
	)

	root := &cobra.Command{
		Use:           "scrutus",
		Short:         "Find inaccurate and useless source comments",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	shared := func(cmd *cobra.Command) {
		flags := cmd.Flags()
		flags.StringVar(&opts.ConfigPath, "config", "", "config file (default: "+config.FileName+", searched upward)")
		flags.BoolVar(&opts.Staged, "staged", false, "scope to files staged in git, limited to staged lines")
		flags.BoolVar(&opts.Changed, "changed", false, "scope to files differing from --base")
		flags.StringVar(&opts.Base, "base", "", "base ref for --changed (default: origin/HEAD, then upstream)")
		flags.StringVar(&opts.BaselinePath, "baseline", "", "baseline file to suppress against")
		flags.StringVar(&opts.Format, "format", "", "text, json, sarif, github, rdjson or checkstyle")
		flags.StringVar(&failOn, "fail-on", "error", "error, warning, info or never")
		flags.StringVar(&opts.Profile, "profile", "", "strict, default or lenient")
		flags.Float64Var(&opts.MinConfidence, "min-confidence", 0, "verdicts below this are downgraded to info")
		flags.Float64Var(&opts.BudgetCents, "budget", 50, "hard stop on estimated spend, in cents")
		flags.IntVar(&opts.Concurrency, "concurrency", 8, "parallel Jev requests")
		flags.BoolVar(&opts.NoCache, "no-cache", false, "bypass cache reads")
		flags.BoolVar(&opts.NoDotenv, "no-dotenv", false, "skip .env loading")
		flags.BoolVar(&opts.SoftFail, "soft-fail", false, "exit 0 on transport failures")
		flags.StringVarP(&outputPath, "output", "o", "", "write the report to a file")
	}

	check := &cobra.Command{
		Use:   "check [paths]",
		Short: "Score and report; exit non-zero on findings",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Mode, opts.Paths = scrutus.ModeCheck, args
			return execute(cmd, &opts, outputPath, failOn)
		},
	}
	shared(check)

	fixCmd := &cobra.Command{
		Use:   "fix [paths]",
		Short: "Score, delete useless comments, report the rest",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Mode, opts.Paths = scrutus.ModeFix, args
			return execute(cmd, &opts, outputPath, failOn)
		},
	}
	shared(fixCmd)
	fixCmd.Flags().BoolVar(&opts.Diff, "diff", false, "print a unified diff instead of writing")
	fixCmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "summarize what would be deleted")
	fixCmd.Flags().IntVar(&opts.DeleteThreshold, "delete-threshold", 0, "override the usefulness cutoff for deletion")
	fixCmd.Flags().BoolVar(&opts.AllowCIWrite, "allow-ci-write", false, "permit writing files when CI is detected")

	baselineCmd := &cobra.Command{
		Use:   "baseline [paths]",
		Short: "Write current findings to baseline.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Mode, opts.Paths = scrutus.ModeBaseline, args
			return execute(cmd, &opts, outputPath, failOn)
		},
	}
	shared(baselineCmd)

	root.AddCommand(check, fixCmd, baselineCmd, cacheCmd(&opts), versionCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "scrutus: %v\n", err)
		return exitCode(err, root)
	}
	return lastExit
}

// lastExit carries the classification outcome out of RunE, which cobra
// reserves for errors.
var lastExit int

func execute(cmd *cobra.Command, opts *scrutus.Options, outputPath, failOn string) error {
	writer := io.Writer(cmd.OutOrStdout())
	if outputPath != "" {
		file, err := os.Create(outputPath)
		if err != nil {
			return err
		}
		defer file.Close()
		writer = file
	} else if opts.Format == "" {
		opts.Format = defaultFormat()
		opts.Color = colorEnabled()
	}
	opts.Out = writer

	report, err := scrutus.Run(cmd.Context(), *opts)
	if err != nil {
		if errors.Is(err, scrutus.ErrSoftFiled) {
			fmt.Fprintf(os.Stderr, "scrutus: %v\n", err)
			return nil
		}
		return err
	}

	if opts.Mode == scrutus.ModeFix && report.Run.Deleted > 0 {
		lastExit = exitFindings
		return nil
	}
	if core.SeverityRank(report.Worst) >= failOnRank(failOn) && failOnRank(failOn) > 0 {
		lastExit = exitFindings
	}
	return nil
}

func failOnRank(failOn string) int {
	switch failOn {
	case "never":
		return 0
	case "info":
		return 1
	case "warning":
		return 2
	default:
		return 3
	}
}

func exitCode(err error, _ *cobra.Command) int {
	var missing *extract.MissingExtractorError
	switch {
	case errors.Is(err, scrutus.ErrBudget):
		return exitBudget
	case errors.As(err, &missing):
		return exitToolError
	default:
		return exitToolError
	}
}

func cacheCmd(opts *scrutus.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "cache", Short: "Manage the local verdict cache"}
	open := func() (*cache.Store, error) {
		cfg, err := configOrDefaults(opts.ConfigPath)
		if err != nil {
			return nil, err
		}
		if !opts.NoDotenv {
			config.LoadDotenv()
		}
		env := typesafe.APIKeyEnv
		if cfg.Jev.APIKeyEnv != "" {
			env = cfg.Jev.APIKeyEnv
		}
		return cache.Open(cfg.Cache.Dir, os.Getenv(env), time.Duration(cfg.Cache.TTL))
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "stats",
		Short: "Print how many verdicts are cached",
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := open()
			if err != nil {
				return err
			}
			defer store.Close()
			count, err := store.Stats()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d cached verdicts\n", count)
			return nil
		},
	}, &cobra.Command{
		Use:   "clear",
		Short: "Drop every cached verdict",
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := open()
			if err != nil {
				return err
			}
			defer store.Close()
			return store.Clear()
		},
	})
	cmd.PersistentFlags().StringVar(&opts.ConfigPath, "config", "", "config file")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, build, rubric version, model and compiled languages",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rubric, err := assess.LoadRubric("default")
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "scrutus %s\nbuild %s\nrubric %d\nmodel %s\nlanguages %s\n",
				scrutus.Version, build(), rubric.Version, typesafe.DefaultModel, strings.Join(extract.Languages(), ", "))
			return nil
		},
	}
}

// build names the commit a binary came from, which the fixed Version cannot
// while releases are rebuilt from every push: the revision go build stamps
// from a checkout, else the module version go install resolved.
func build() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if revision := settings["vcs.revision"]; revision != "" {
		if settings["vcs.modified"] == "true" {
			revision += "-dirty"
		}
		return revision
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "unknown"
}

func configOrDefaults(path string) (config.Config, error) {
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return config.Config{}, err
		}
		if found, err := config.Find(cwd); err == nil && found != "" {
			path = found
		}
	}
	if path == "" {
		return config.Defaults(), nil
	}
	return config.Load(path)
}

func defaultFormat() string {
	if isTerminal() {
		return "text"
	}
	return "json"
}

func colorEnabled() bool {
	return isTerminal() && os.Getenv("NO_COLOR") == ""
}

func isTerminal() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
