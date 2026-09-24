package report

import (
	"encoding/json"
	"io"

	"github.com/SergeAx/scrutus/internal/core"
)

type JSON struct{}

type jsonReport struct {
	SchemaVersion int            `json:"schema_version"`
	Tool          jsonTool       `json:"tool"`
	Backend       jsonBackend    `json:"backend"`
	Run           jsonRun        `json:"run"`
	Results       []jsonResult   `json:"results"`
	Summary       map[string]int `json:"summary"`
}

type jsonTool struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	RubricVersion int    `json:"rubric_version"`
}

type jsonBackend struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

type jsonRun struct {
	Mode        string  `json:"mode"`
	Scope       string  `json:"scope"`
	Base        string  `json:"base,omitempty"`
	Files       int     `json:"files"`
	Comments    int     `json:"comments"`
	Cached      int     `json:"cached"`
	Assessed    int     `json:"assessed"`
	Requests    int     `json:"requests"`
	Suppressed  int     `json:"suppressed"`
	Baselined   int     `json:"baselined"`
	InputTokens int     `json:"input_tokens"`
	CostUSD     float64 `json:"cost_usd"`
	CostKnown   bool    `json:"cost_known"`
	DurationMS  int64   `json:"duration_ms"`
	Incomplete  bool    `json:"incomplete,omitempty"`
}

type jsonResult struct {
	ID         string    `json:"id"`
	File       string    `json:"file"`
	Line       int       `json:"line"`
	Column     int       `json:"column"`
	Span       core.Span `json:"span"`
	Kind       string    `json:"kind"`
	Rule       string    `json:"rule"`
	Severity   string    `json:"severity"`
	Action     string    `json:"action"`
	Accuracy   jsonAxis  `json:"accuracy"`
	Usefulness jsonAxis  `json:"usefulness"`
	Overreach  jsonNoul  `json:"overreach"`
	Comment    string    `json:"comment"`
	Code       string    `json:"code"`
}

type jsonAxis struct {
	Pct        int     `json:"pct"`
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
}

type jsonNoul struct {
	Prob float64 `json:"prob"`
}

func (JSON) Report(w io.Writer, results []core.Result, run core.RunInfo) error {
	reportable := Reportable(results)
	out := jsonReport{
		SchemaVersion: SchemaVersion,
		Tool:          jsonTool{Name: "scrutus", Version: run.Version, RubricVersion: run.RubricVer},
		Backend:       jsonBackend{Name: "jev", Model: run.Model},
		Run: jsonRun{
			Mode: run.Mode, Scope: run.Scope, Base: run.Base,
			Files: run.Files, Comments: run.Comments,
			Cached: run.Cached, Assessed: run.Assessed, Requests: run.Requests,
			Suppressed: run.Suppressed, Baselined: run.Baselined,
			InputTokens: run.InputTokens, CostUSD: run.CostUSD,
			CostKnown: !run.UsageUnknown, DurationMS: run.DurationMS,
			Incomplete: run.Incomplete,
		},
		Results: make([]jsonResult, 0, len(reportable)),
		Summary: Summary(results),
	}

	for _, r := range reportable {
		f, v := r.Finding, r.Verdict
		out.Results = append(out.Results, jsonResult{
			ID: f.ID, File: f.File, Line: f.Line, Column: f.Column,
			Span: f.Comment, Kind: string(f.Kind),
			Rule: r.Rule, Severity: string(r.Severity), Action: string(r.Action),
			Accuracy:   jsonAxis{v.Accuracy.Pct, v.Accuracy.Label, v.Accuracy.Confidence},
			Usefulness: jsonAxis{v.Usefulness.Pct, v.Usefulness.Label, v.Usefulness.Confidence},
			Overreach:  jsonNoul{v.Overreach.Prob},
			Comment:    snippet(f.CommentText, 200),
			Code:       snippet(f.CodeText, 200),
		})
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}
