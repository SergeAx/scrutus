// Package filter drops what must never be scored, before any network call.
package filter

import (
	"strings"

	"github.com/SergeAx/scrutus/internal/config"
	"github.com/SergeAx/scrutus/internal/core"
)

const (
	ignoreMarker     = "scrutus:ignore"
	ignoreNextMarker = "scrutus:ignore-next"
)

type Counts struct {
	Suppressed int
	Baselined  int
	Exempt     int
}

type Baseline interface {
	Has(id string) bool
}

// Apply keeps the findings worth scoring. Exempt and suppressed comments are
// dropped silently; baselined ones are counted so CI can trend them.
func Apply(findings []core.Finding, src map[string][]byte, cfg config.Comments, baseline Baseline) ([]core.Finding, Counts) {
	var counts Counts
	kept := make([]core.Finding, 0, len(findings))
	for _, f := range findings {
		if !cfg.AllowsKind(string(f.Kind)) {
			counts.Exempt++
			continue
		}
		text := Text(f.CommentText)
		if isExempt(text, cfg.ExemptPrefixes) {
			counts.Exempt++
			continue
		}
		// min_chars never applies to annotations: "@var Foo" is short by design.
		if f.Kind != core.KindAnnotation && len([]rune(text)) < cfg.MinChars {
			counts.Exempt++
			continue
		}
		if suppressed(f, src[f.File]) {
			counts.Suppressed++
			continue
		}
		if baseline != nil && baseline.Has(f.ID) {
			counts.Baselined++
			continue
		}
		kept = append(kept, f)
	}
	return kept, counts
}

// Text strips comment markers so prefixes and lengths are measured on what a
// human actually wrote.
func Text(comment string) string {
	lines := strings.Split(comment, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "/**")
		line = strings.TrimPrefix(line, "/*")
		line = strings.TrimSuffix(line, "*/")
		line = strings.TrimPrefix(line, "//")
		line = strings.TrimPrefix(line, "#")
		line = strings.TrimPrefix(line, "*")
		out = append(out, strings.TrimSpace(line))
	}
	return strings.TrimSpace(strings.Join(out, " "))
}

func isExempt(text string, prefixes []string) bool {
	if text == "" {
		return true
	}
	lower := strings.ToLower(text)
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func suppressed(f core.Finding, src []byte) bool {
	if strings.Contains(f.CommentText, ignoreMarker) {
		return true
	}
	if len(src) == 0 {
		return false
	}
	before := src[:min(f.Comment.Start, len(src))]
	lines := strings.Split(string(before), "\n")
	for i := len(lines) - 2; i >= 0 && i >= len(lines)-3; i-- {
		if strings.Contains(lines[i], ignoreNextMarker) {
			return true
		}
	}
	return false
}
