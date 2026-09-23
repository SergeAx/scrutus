// Package scope decides which files, and which lines of them, are in play.
package scope

import (
	"bufio"
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type LineRange struct{ Start, End int }

type File struct {
	Path string
	// Ranges limits scoring to comments intersecting these lines; nil means
	// the whole file.
	Ranges []LineRange
}

type Options struct {
	Paths   []string
	Staged  bool
	Changed bool
	Base    string
	Ignore  []string
}

type Result struct {
	Files []File
	Mode  string
	Base  string
}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".idea": true, ".vscode": true,
}

func Resolve(opts Options) (Result, error) {
	switch {
	case opts.Staged:
		files, err := fromGit(opts, []string{"diff", "--cached", "--name-only", "--diff-filter=ACMR"},
			[]string{"diff", "--cached", "-U0", "--diff-filter=ACMR"})
		return Result{Files: files, Mode: "staged"}, err
	case opts.Changed:
		base, err := ResolveBase(opts.Base)
		if err != nil {
			return Result{}, err
		}
		files, err := fromGit(opts,
			[]string{"diff", "--name-only", "--diff-filter=ACMR", base + "...HEAD"},
			[]string{"diff", "-U0", "--diff-filter=ACMR", base + "...HEAD"})
		return Result{Files: files, Mode: "changed", Base: base}, err
	default:
		files, err := fromPaths(opts)
		return Result{Files: files, Mode: "paths"}, err
	}
}

// ResolveBase finds the ref --changed compares against: whatever the remote
// calls its default branch, then the upstream, then the configured default,
// then main or master. A silently wrong base yields an empty run, so the
// resolved value is reported.
func ResolveBase(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if ref, err := git("symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(strings.TrimSpace(ref), "refs/remotes/"); name != "" {
			return name, nil
		}
	}
	if upstream, err := git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		if name := strings.TrimSpace(upstream); name != "" {
			return name, nil
		}
	}
	if configured, err := git("config", "--get", "init.defaultBranch"); err == nil {
		if name := strings.TrimSpace(configured); name != "" {
			if _, err := git("rev-parse", "--verify", name); err == nil {
				return name, nil
			}
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if _, err := git("rev-parse", "--verify", candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no base ref found: pass --base")
}

func fromGit(opts Options, nameArgs, diffArgs []string) ([]File, error) {
	names, err := git(nameArgs...)
	if err != nil {
		return nil, err
	}
	diff, err := git(diffArgs...)
	if err != nil {
		return nil, err
	}
	ranges := parseHunks(diff)

	var files []File
	for name := range strings.SplitSeq(strings.ReplaceAll(names, "\r\n", "\n"), "\n") {
		name = strings.TrimSpace(name)
		if name == "" || ignored(name, opts.Ignore) {
			continue
		}
		if info, err := os.Stat(name); err != nil || info.IsDir() {
			continue
		}
		files = append(files, File{Path: filepath.Clean(name), Ranges: ranges[name]})
	}
	return files, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -\S+ \+(\d+)(?:,(\d+))? @@`)

func parseHunks(diff string) map[string][]LineRange {
	out := map[string][]LineRange{}
	current := ""
	scanner := bufio.NewScanner(strings.NewReader(diff))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			current = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "@@") && current != "":
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			if count == 0 {
				continue
			}
			out[current] = append(out[current], LineRange{Start: start, End: start + count - 1})
		}
	}
	return out
}

func fromPaths(opts Options) ([]File, error) {
	paths := opts.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}

	seen := map[string]bool{}
	var files []File
	for _, raw := range paths {
		root := strings.TrimSuffix(raw, "/...")
		if root == "" {
			root = "."
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if !ignored(root, opts.Ignore) && !seen[root] {
				seen[root] = true
				files = append(files, File{Path: filepath.Clean(root)})
			}
			continue
		}
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			clean := filepath.Clean(path)
			if ignored(clean, opts.Ignore) || seen[clean] {
				return nil
			}
			seen[clean] = true
			files = append(files, File{Path: clean})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	slices.SortFunc(files, func(a, b File) int { return cmp.Compare(a.Path, b.Path) })
	return files, nil
}

func ignored(path string, patterns []string) bool {
	slashed := filepathSlash(path)
	for _, pattern := range patterns {
		if MatchGlob(pattern, slashed) {
			return true
		}
	}
	return false
}

// Intersects reports whether a comment on these lines is in scope.
func Intersects(ranges []LineRange, startLine, endLine int) bool {
	if len(ranges) == 0 {
		return true
	}
	for _, r := range ranges {
		if startLine <= r.End && endLine >= r.Start {
			return true
		}
	}
	return false
}

func git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
