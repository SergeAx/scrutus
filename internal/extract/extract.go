// Package extract turns source files into comment/code pairs.
package extract

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/SergeAx/scrutus/internal/core"
)

type Options struct {
	ContextLines int
	MaxCodeLines int
}

func (o Options) withDefaults() Options {
	if o.ContextLines == 0 {
		o.ContextLines = 20
	}
	if o.MaxCodeLines == 0 {
		o.MaxCodeLines = 40
	}
	return o
}

type Extractor interface {
	Languages() []string
	Extensions() []string
	Extract(file string, src []byte, opts Options) ([]core.Finding, error)
}

var registry []Extractor

func Register(e Extractor) { registry = append(registry, e) }

// Languages reports which languages this build can parse, which is not the
// whole list in §4.3 unless the tree-sitter extractors were compiled in.
func Languages() []string {
	var names []string
	for _, e := range registry {
		names = append(names, e.Languages()...)
	}
	slices.Sort(names)
	return names
}

func For(file string) (Extractor, bool) {
	ext := strings.ToLower(filepath.Ext(file))
	for _, e := range registry {
		if slices.Contains(e.Extensions(), ext) {
			return e, true
		}
	}
	return nil, false
}

// LanguageOf names the language a path belongs to even when no extractor for
// it was compiled in, so a missing extractor can be reported instead of
// skipping the file.
func LanguageOf(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".php":
		return "php"
	case ".py", ".pyi":
		return "python"
	}
	return ""
}

type MissingExtractorError struct {
	Language string
	File     string
}

func (e *MissingExtractorError) Error() string {
	return fmt.Sprintf("%s: this build has no %s extractor; use a release binary "+
		"or build with cgo enabled (compiled: %s)",
		e.File, e.Language, strings.Join(Languages(), ", "))
}

// Extract dispatches on the file's extension. A language the config asks for
// but this build cannot parse is an error, never a silent skip.
func Extract(file string, src []byte, languages []string, opts Options) ([]core.Finding, error) {
	opts = opts.withDefaults()
	if e, ok := For(file); ok {
		return e.Extract(file, src, opts)
	}
	lang := LanguageOf(file)
	if lang != "" && slices.Contains(languages, lang) {
		return nil, &MissingExtractorError{Language: lang, File: file}
	}
	return nil, nil
}
