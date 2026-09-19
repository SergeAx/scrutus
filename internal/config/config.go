// Package config loads .scrutus.toml, applies profiles and defaults, and
// reports unknown keys rather than silently keeping a default.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const FileName = ".scrutus.toml"

type Config struct {
	Version    int         `toml:"version"`
	Rubric     string      `toml:"rubric"`
	Profile    string      `toml:"profile"`
	Languages  []string    `toml:"languages"`
	Ignore     []string    `toml:"ignore"`
	Jev        Jev         `toml:"jev"`
	Thresholds Thresholds  `toml:"thresholds"`
	Comments   Comments    `toml:"comments"`
	Cache      CacheConfig `toml:"cache"`

	// Path is the file this config was read from, empty for the defaults.
	Path string `toml:"-"`
}

type Jev struct {
	Model      string   `toml:"model"`
	APIKeyEnv  string   `toml:"api_key_env"`
	RawTimeout string   `toml:"timeout"`
	Timeout    Duration `toml:"-"`
}

type Thresholds struct {
	Accuracy      Accuracy   `toml:"accuracy"`
	Usefulness    Usefulness `toml:"usefulness"`
	MinConfidence *float64   `toml:"min_confidence"`
}

type Accuracy struct {
	Error   *int `toml:"error"`
	Warning *int `toml:"warning"`
}

type Usefulness struct {
	Delete  *int `toml:"delete"`
	Warning *int `toml:"warning"`
}

type Comments struct {
	Kinds             []string `toml:"kinds"`
	ExemptPrefixes    []string `toml:"exempt_prefixes"`
	MinChars          int      `toml:"min_chars"`
	ContextLines      int      `toml:"context_lines"`
	KeepExportedDocs  *bool    `toml:"keep_exported_docs"`
	KeepDocstrings    *bool    `toml:"keep_docstrings"`
	DeleteAnnotations bool     `toml:"delete_annotations"`
}

type CacheConfig struct {
	Dir    string   `toml:"dir"`
	RawTTL string   `toml:"ttl"`
	TTL    Duration `toml:"-"`
	Shared bool     `toml:"shared"`
}

type Duration time.Duration

// ParseDuration accepts time.ParseDuration's units plus "d" for days, which
// TOML has no native type for and 90-day cache TTLs need.
func ParseDuration(s string) (Duration, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(rest, 64)
		if err == nil {
			return Duration(time.Duration(days * 24 * float64(time.Hour))), nil
		}
	}
	d, err := time.ParseDuration(s)
	return Duration(d), err
}

type Profile struct {
	Name             string
	AccuracyError    int
	AccuracyWarning  int
	UsefulnessDelete int
	UsefulnessWarn   int
	MinConfidence    float64
}

var profiles = map[string]Profile{
	"strict":  {"strict", 40, 70, 25, 45, 0.4},
	"default": {"default", 30, 60, 15, 35, 0.5},
	"lenient": {"lenient", 20, 45, 8, 20, 0.65},
}

func ProfileNames() []string { return []string{"strict", "default", "lenient"} }

// Resolved is the config after the profile, defaults and flags have been
// folded in; every stage reads this, never the raw file.
type Resolved struct {
	Config
	AccuracyError    int
	AccuracyWarning  int
	UsefulnessDelete int
	UsefulnessWarn   int
	MinConfidence    float64
}

func Defaults() Config {
	keepExported, keepDocstrings := true, true
	return Config{
		Version:   1,
		Rubric:    "default",
		Profile:   "default",
		Languages: []string{"go", "typescript", "javascript", "php", "python"},
		Ignore:    []string{"vendor/**", "**/*_test.go", "**/generated/**"},
		Jev: Jev{
			Model:   "jev-latest",
			Timeout: Duration(10 * time.Second),
		},
		Comments: Comments{
			Kinds: []string{"doc", "inline", "trailing", "annotation"},
			ExemptPrefixes: []string{
				"TODO", "FIXME", "HACK", "XXX", "scrutus:",
				"Copyright", "SPDX-License-Identifier", "@license", "@preserve", "!",
				"-*-", "vim:", "coding:", "coding=",
				"nolint", "go:",
				"phpcs:", "@phpstan-", "@psalm-",
				"eslint-", "@ts-", "prettier-ignore", "biome-ignore", "oxlint-", "tslint:",
				"jshint", "deno-", "istanbul ", "c8 ", "webpack", "sourceMappingURL",
				"sourceURL", "@jsx", "@flow", "@jest-", "@vitest-",
				"type: ignore", "noqa", "pragma:", "pylint:", "mypy:", "ruff:",
				"pyright:", "pyre-", "fmt:", "isort:", "yapf:", "nosec",
			},
			MinChars:         12,
			ContextLines:     20,
			KeepExportedDocs: &keepExported,
			KeepDocstrings:   &keepDocstrings,
		},
		Cache: CacheConfig{
			Dir: defaultCacheDir(),
			TTL: Duration(90 * 24 * time.Hour),
		},
	}
}

func defaultCacheDir() string {
	if dir := os.Getenv("SCRUTUS_CACHE_DIR"); dir != "" {
		return dir
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "scrutus")
	}
	return filepath.Join(base, "scrutus")
}

// Find walks up from dir looking for a config file, stopping at a repo root.
func Find(dir string) (string, error) {
	for {
		candidate := filepath.Join(dir, FileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	var file Config
	decoder := toml.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return cfg, fmt.Errorf("%s: %s", path, strict.String())
		}
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if file.Version != 0 && file.Version != 1 {
		return cfg, fmt.Errorf("%s: unsupported version %d", path, file.Version)
	}

	merge(&cfg, file)
	cfg.Path = path
	return cfg, cfg.parseDurations(path)
}

func (c *Config) parseDurations(path string) error {
	if c.Jev.RawTimeout != "" {
		d, err := ParseDuration(c.Jev.RawTimeout)
		if err != nil {
			return fmt.Errorf("%s: jev.timeout: %w", path, err)
		}
		c.Jev.Timeout = d
	}
	if c.Cache.RawTTL != "" {
		d, err := ParseDuration(c.Cache.RawTTL)
		if err != nil {
			return fmt.Errorf("%s: cache.ttl: %w", path, err)
		}
		c.Cache.TTL = d
	}
	return nil
}

func merge(dst *Config, src Config) {
	if src.Rubric != "" {
		dst.Rubric = src.Rubric
	}
	if src.Profile != "" {
		dst.Profile = src.Profile
	}
	if src.Languages != nil {
		dst.Languages = src.Languages
	}
	if src.Ignore != nil {
		dst.Ignore = src.Ignore
	}
	if src.Jev.Model != "" {
		dst.Jev.Model = src.Jev.Model
	}
	if src.Jev.APIKeyEnv != "" {
		dst.Jev.APIKeyEnv = src.Jev.APIKeyEnv
	}
	if src.Jev.RawTimeout != "" {
		dst.Jev.RawTimeout = src.Jev.RawTimeout
	}
	dst.Thresholds = mergeThresholds(dst.Thresholds, src.Thresholds)
	if src.Comments.Kinds != nil {
		dst.Comments.Kinds = src.Comments.Kinds
	}
	if src.Comments.ExemptPrefixes != nil {
		dst.Comments.ExemptPrefixes = src.Comments.ExemptPrefixes
	}
	if src.Comments.MinChars != 0 {
		dst.Comments.MinChars = src.Comments.MinChars
	}
	if src.Comments.ContextLines != 0 {
		dst.Comments.ContextLines = src.Comments.ContextLines
	}
	if src.Comments.KeepExportedDocs != nil {
		dst.Comments.KeepExportedDocs = src.Comments.KeepExportedDocs
	}
	if src.Comments.KeepDocstrings != nil {
		dst.Comments.KeepDocstrings = src.Comments.KeepDocstrings
	}
	dst.Comments.DeleteAnnotations = src.Comments.DeleteAnnotations
	if src.Cache.Dir != "" {
		dst.Cache.Dir = src.Cache.Dir
	}
	if src.Cache.RawTTL != "" {
		dst.Cache.RawTTL = src.Cache.RawTTL
	}
	dst.Cache.Shared = src.Cache.Shared
}

func mergeThresholds(dst, src Thresholds) Thresholds {
	if src.Accuracy.Error != nil {
		dst.Accuracy.Error = src.Accuracy.Error
	}
	if src.Accuracy.Warning != nil {
		dst.Accuracy.Warning = src.Accuracy.Warning
	}
	if src.Usefulness.Delete != nil {
		dst.Usefulness.Delete = src.Usefulness.Delete
	}
	if src.Usefulness.Warning != nil {
		dst.Usefulness.Warning = src.Usefulness.Warning
	}
	if src.MinConfidence != nil {
		dst.MinConfidence = src.MinConfidence
	}
	return dst
}

// Resolve folds the profile under the explicit thresholds: defaults lose to
// the profile, the profile loses to [thresholds], and both lose to flags.
func (c Config) Resolve() (Resolved, error) {
	profile, ok := profiles[c.Profile]
	if !ok {
		return Resolved{}, fmt.Errorf("unknown profile %q, want one of %s",
			c.Profile, strings.Join(ProfileNames(), ", "))
	}

	r := Resolved{
		Config:           c,
		AccuracyError:    profile.AccuracyError,
		AccuracyWarning:  profile.AccuracyWarning,
		UsefulnessDelete: profile.UsefulnessDelete,
		UsefulnessWarn:   profile.UsefulnessWarn,
		MinConfidence:    profile.MinConfidence,
	}
	if v := c.Thresholds.Accuracy.Error; v != nil {
		r.AccuracyError = *v
	}
	if v := c.Thresholds.Accuracy.Warning; v != nil {
		r.AccuracyWarning = *v
	}
	if v := c.Thresholds.Usefulness.Delete; v != nil {
		r.UsefulnessDelete = *v
	}
	if v := c.Thresholds.Usefulness.Warning; v != nil {
		r.UsefulnessWarn = *v
	}
	if v := c.Thresholds.MinConfidence; v != nil {
		r.MinConfidence = *v
	}
	return r, nil
}

func (c Comments) KeepExported() bool {
	return c.KeepExportedDocs == nil || *c.KeepExportedDocs
}

func (c Comments) KeepDocstring() bool {
	return c.KeepDocstrings == nil || *c.KeepDocstrings
}

func (c Comments) AllowsKind(kind string) bool {
	for _, k := range c.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}
