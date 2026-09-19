package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SergeAx/scrutus/internal/config"
)

func write(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestExplicitThresholdsOverrideTheProfile(t *testing.T) {
	path := write(t, `
version = 1
profile = "lenient"

[thresholds]
min_confidence = 0.55
usefulness = { delete = 22 }
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if resolved.UsefulnessDelete != 22 {
		t.Errorf("usefulness delete = %d, want the explicit 22", resolved.UsefulnessDelete)
	}
	if resolved.MinConfidence != 0.55 {
		t.Errorf("min_confidence = %v, want the explicit 0.55", resolved.MinConfidence)
	}
	// Untouched values still come from the profile.
	if resolved.AccuracyError != 20 || resolved.UsefulnessWarn != 20 {
		t.Errorf("profile values lost: accuracy error %d, usefulness warning %d",
			resolved.AccuracyError, resolved.UsefulnessWarn)
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	path := write(t, "version = 1\nprofle = \"strict\"\n")

	if _, err := config.Load(path); err == nil {
		t.Fatal("a misspelled key was accepted")
	} else if !strings.Contains(err.Error(), "profle") {
		t.Errorf("error = %v, want it to name the unknown key", err)
	}
}

func TestUnknownProfileIsAnError(t *testing.T) {
	cfg := config.Defaults()
	cfg.Profile = "medium"

	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("an unknown profile was accepted")
	}
}

func TestDurationsAcceptDays(t *testing.T) {
	path := write(t, "version = 1\n\n[jev]\ntimeout = \"3s\"\n\n[cache]\nttl = \"90d\"\n")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if time.Duration(cfg.Jev.Timeout) != 3*time.Second {
		t.Errorf("timeout = %s, want 3s", time.Duration(cfg.Jev.Timeout))
	}
	if time.Duration(cfg.Cache.TTL) != 90*24*time.Hour {
		t.Errorf("ttl = %s, want 2160h", time.Duration(cfg.Cache.TTL))
	}
}

// The key never lives in config: only the name of the variable holding it.
func TestAPIKeyEnvIsOptional(t *testing.T) {
	path := write(t, "version = 1\n\n[jev]\nmodel = \"jev-1.13.0\"\n")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Jev.APIKeyEnv != "" {
		t.Errorf("api_key_env = %q, want empty so the SDK reads its own default", cfg.Jev.APIKeyEnv)
	}
}

func TestDotenvNeverOverridesTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SCRUTUS_TEST_SET=from-file\nSCRUTUS_TEST_UNSET=from-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("SCRUTUS_TEST_SET", "from-environment")
	os.Unsetenv("SCRUTUS_TEST_UNSET")

	config.LoadDotenv()

	if got := os.Getenv("SCRUTUS_TEST_SET"); got != "from-environment" {
		t.Errorf("SCRUTUS_TEST_SET = %q, want the environment to win", got)
	}
	if got := os.Getenv("SCRUTUS_TEST_UNSET"); got != "from-file" {
		t.Errorf("SCRUTUS_TEST_UNSET = %q, want the file to fill the gap", got)
	}
	os.Unsetenv("SCRUTUS_TEST_UNSET")
}

func TestFindStopsAtTheRepoRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "pkg", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), config.FileName)
	if err := os.WriteFile(outside, []byte("version = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	found, err := config.Find(nested)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found != "" {
		t.Errorf("found %q above the repo root", found)
	}
}
