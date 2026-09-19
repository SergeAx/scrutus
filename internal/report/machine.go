package report

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/SergeAx/scrutus/internal/core"
)

// SARIF 2.1.0, hand-rolled: the subset scrutus emits is small and staying
// dependency-free keeps the default build cgo-free and go install-clean.
type SARIF struct{}

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string          `json:"id"`
	ShortDescription sarifText       `json:"shortDescription"`
	DefaultConfig    sarifRuleConfig `json:"defaultConfiguration"`
}

type sarifRuleConfig struct {
	Level string `json:"level"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifText       `json:"message"`
	Locations []sarifLocation `json:"locations"`
	Fixes     []sarifFix      `json:"fixes,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           sarifRegion   `json:"region"`
}

type sarifArtifact struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn,omitempty"`
	ByteOffset  int `json:"byteOffset"`
	ByteLength  int `json:"byteLength"`
}

type sarifFix struct {
	Description sarifText          `json:"description"`
	Changes     []sarifArtifactFix `json:"artifactChanges"`
}

type sarifArtifactFix struct {
	ArtifactLocation sarifArtifact      `json:"artifactLocation"`
	Replacements     []sarifReplacement `json:"replacements"`
}

type sarifReplacement struct {
	DeletedRegion sarifRegion `json:"deletedRegion"`
}

var sarifLevels = map[core.Severity]string{
	core.SeverityError:   "error",
	core.SeverityWarning: "warning",
	core.SeverityInfo:    "note",
}

func (SARIF) Report(w io.Writer, results []core.Result, run core.RunInfo) error {
	reportable := Reportable(results)
	rules := map[string]sarifRule{}
	out := sarifLog{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs:    []sarifRun{{Tool: sarifTool{Driver: sarifDriver{Name: "scrutus", Version: run.Version, InformationURI: "https://github.com/SergeAx/scrutus"}}}},
	}

	for _, r := range reportable {
		f := r.Finding
		level := sarifLevels[r.Severity]
		rules[r.Rule] = sarifRule{
			ID:               r.Rule,
			ShortDescription: sarifText{Text: ruleDescription(r.Rule)},
			DefaultConfig:    sarifRuleConfig{Level: level},
		}
		region := sarifRegion{
			StartLine:   f.Line,
			StartColumn: f.Column,
			ByteOffset:  f.Comment.Start,
			ByteLength:  f.Comment.End - f.Comment.Start,
		}
		result := sarifResult{
			RuleID:  r.Rule,
			Level:   level,
			Message: sarifText{Text: message(r)},
			Locations: []sarifLocation{{PhysicalLocation: sarifPhysical{
				ArtifactLocation: sarifArtifact{URI: uri(f.File)},
				Region:           region,
			}}},
		}
		if r.Action == core.ActionDelete {
			result.Fixes = []sarifFix{{
				Description: sarifText{Text: "Delete the comment"},
				Changes: []sarifArtifactFix{{
					ArtifactLocation: sarifArtifact{URI: uri(f.File)},
					Replacements:     []sarifReplacement{{DeletedRegion: region}},
				}},
			}}
		}
		out.Runs[0].Results = append(out.Runs[0].Results, result)
	}

	for _, rule := range rules {
		out.Runs[0].Tool.Driver.Rules = append(out.Runs[0].Tool.Driver.Rules, rule)
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

type GitHub struct{}

func (GitHub) Report(w io.Writer, results []core.Result, _ core.RunInfo) error {
	for _, r := range Reportable(results) {
		f := r.Finding
		level := "notice"
		switch r.Severity {
		case core.SeverityError:
			level = "error"
		case core.SeverityWarning:
			level = "warning"
		}
		fmt.Fprintf(w, "::%s file=%s,line=%d,col=%d,title=%s::%s\n",
			level, uri(f.File), f.Line, f.Column, r.Rule, message(r))
	}
	return nil
}

type RDJSON struct{}

type rdjsonReport struct {
	Source      rdjsonSource      `json:"source"`
	Severity    string            `json:"severity"`
	Diagnostics []rdjsonDiagnosic `json:"diagnostics"`
}

type rdjsonSource struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type rdjsonDiagnosic struct {
	Message     string             `json:"message"`
	Location    rdjsonLocation     `json:"location"`
	Severity    string             `json:"severity"`
	Code        rdjsonCode         `json:"code"`
	Suggestions []rdjsonSuggestion `json:"suggestions,omitempty"`
}

type rdjsonCode struct {
	Value string `json:"value"`
}

type rdjsonLocation struct {
	Path  string      `json:"path"`
	Range rdjsonRange `json:"range"`
}

type rdjsonRange struct {
	Start rdjsonPos `json:"start"`
	End   rdjsonPos `json:"end"`
}

type rdjsonPos struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type rdjsonSuggestion struct {
	Range rdjsonRange `json:"range"`
	Text  string      `json:"text"`
}

func (RDJSON) Report(w io.Writer, results []core.Result, _ core.RunInfo) error {
	out := rdjsonReport{
		Source:   rdjsonSource{Name: "scrutus", URL: "https://github.com/SergeAx/scrutus"},
		Severity: "WARNING",
	}
	for _, r := range Reportable(results) {
		f := r.Finding
		diagnostic := rdjsonDiagnosic{
			Message:  message(r),
			Severity: strings.ToUpper(string(r.Severity)),
			Code:     rdjsonCode{Value: r.Rule},
			Location: rdjsonLocation{
				Path: uri(f.File),
				Range: rdjsonRange{
					Start: rdjsonPos{Line: f.Line, Column: f.Column},
					End:   rdjsonPos{Line: f.Line, Column: f.Column},
				},
			},
		}
		if r.Action == core.ActionDelete {
			diagnostic.Suggestions = []rdjsonSuggestion{{
				Range: diagnostic.Location.Range,
				Text:  "",
			}}
		}
		out.Diagnostics = append(out.Diagnostics, diagnostic)
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

type Checkstyle struct{}

type checkstyleReport struct {
	XMLName xml.Name         `xml:"checkstyle"`
	Version string           `xml:"version,attr"`
	Files   []checkstyleFile `xml:"file"`
}

type checkstyleFile struct {
	Name   string            `xml:"name,attr"`
	Errors []checkstyleError `xml:"error"`
}

type checkstyleError struct {
	Line     int    `xml:"line,attr"`
	Column   int    `xml:"column,attr"`
	Severity string `xml:"severity,attr"`
	Message  string `xml:"message,attr"`
	Source   string `xml:"source,attr"`
}

func (Checkstyle) Report(w io.Writer, results []core.Result, _ core.RunInfo) error {
	byFile := map[string][]checkstyleError{}
	var order []string
	for _, r := range Reportable(results) {
		f := r.Finding
		path := uri(f.File)
		if _, seen := byFile[path]; !seen {
			order = append(order, path)
		}
		byFile[path] = append(byFile[path], checkstyleError{
			Line: f.Line, Column: f.Column,
			Severity: string(r.Severity), Message: message(r), Source: "scrutus." + r.Rule,
		})
	}

	out := checkstyleReport{Version: "8.0"}
	for _, path := range order {
		out.Files = append(out.Files, checkstyleFile{Name: path, Errors: byFile[path]})
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(out); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func message(r core.Result) string {
	v := r.Verdict
	return fmt.Sprintf("%s (accuracy %d%%, usefulness %d%%): %s",
		ruleDescription(r.Rule), v.Accuracy.Pct, v.Usefulness.Pct, snippet(r.Finding.CommentText, 80))
}

func ruleDescription(rule string) string {
	switch rule {
	case core.RuleWrongComment:
		return "comment does not describe the code"
	case core.RuleUselessComment:
		return "comment says nothing the code does not"
	case core.RuleRedundantAnnotation:
		return "annotation restates the code"
	case core.RuleWeakComment:
		return "comment is weak on one axis"
	case core.RuleWideScope:
		return "comment is scoped wider than the code it pairs with"
	case core.RuleLowConfidence:
		return "verdict below the confidence threshold"
	case core.RuleFixAborted:
		return "edits discarded: the file stopped parsing"
	}
	return rule
}

func uri(path string) string { return filepath.ToSlash(path) }
