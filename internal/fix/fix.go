// Package fix deletes the comments classification marked Delete, and nothing
// else.
package fix

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
)

type FileResult struct {
	Path    string
	Deleted int
	Diff    string
	Err     error
}

type Options struct {
	DryRun    bool
	Languages []string
}

// Apply plans and writes the deletions. Spans are applied back to front so
// earlier offsets stay valid, and a file that stops parsing keeps its
// original bytes.
func Apply(results []core.Result, src map[string][]byte, opts Options) ([]FileResult, error) {
	byFile := map[string][]core.Result{}
	var order []string
	for _, r := range results {
		if r.Action != core.ActionDelete {
			continue
		}
		path := r.Finding.File
		if _, seen := byFile[path]; !seen {
			order = append(order, path)
		}
		byFile[path] = append(byFile[path], r)
	}
	sort.Strings(order)

	out := make([]FileResult, 0, len(order))
	for _, path := range order {
		out = append(out, applyFile(path, byFile[path], src[path], opts))
	}
	return out, nil
}

func applyFile(path string, results []core.Result, original []byte, opts Options) FileResult {
	result := FileResult{Path: path}
	if original == nil {
		content, err := os.ReadFile(path)
		if err != nil {
			result.Err = err
			return result
		}
		original = content
	}

	spans := make([]core.Span, 0, len(results))
	for _, r := range results {
		spans = append(spans, widen(original, r.Finding.Comment))
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start > spans[j].Start })

	edited := append([]byte(nil), original...)
	for _, span := range spans {
		if span.Start < 0 || span.End > len(edited) || span.Start > span.End {
			result.Err = fmt.Errorf("%s: span %d-%d out of range", path, span.Start, span.End)
			return result
		}
		edited = append(edited[:span.Start], edited[span.End:]...)
	}

	if _, err := extract.Extract(path, edited, opts.Languages, extract.Options{}); err != nil {
		result.Err = fmt.Errorf("%s: %s: edits discarded: %w", path, core.RuleFixAborted, err)
		return result
	}

	result.Deleted = len(spans)
	result.Diff = UnifiedDiff(path, original, spans)
	if opts.DryRun {
		return result
	}
	if err := writeAtomic(path, original, edited); err != nil {
		result.Err = err
	}
	return result
}

// widen takes the whole line when the comment is alone on it, and otherwise
// trims back to the code that shares the line.
func widen(src []byte, span core.Span) core.Span {
	lineStart := bytes.LastIndexByte(src[:span.Start], '\n') + 1
	prefix := src[lineStart:span.Start]

	end := span.End
	if newline := bytes.IndexByte(src[span.End:], '\n'); newline >= 0 {
		trailing := src[span.End : span.End+newline]
		if len(bytes.TrimSpace(trailing)) == 0 {
			end = span.End + newline + 1
		}
	} else {
		end = len(src)
	}

	if len(bytes.TrimSpace(prefix)) == 0 {
		return core.Span{Start: lineStart, End: end}
	}
	trimmed := span.Start
	for trimmed > lineStart && (src[trimmed-1] == ' ' || src[trimmed-1] == '\t') {
		trimmed--
	}
	return core.Span{Start: trimmed, End: span.End}
}

func writeAtomic(path string, expected, content []byte) error {
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if sha256.Sum256(current) != sha256.Sum256(expected) {
		return fmt.Errorf("%s: changed on disk during the run, skipped", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".scrutus-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())

	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), info.Mode()); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
