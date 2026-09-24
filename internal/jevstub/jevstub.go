// Package jevstub replays recorded Jev answers over HTTP, so tests exercise
// the real SDK path without a network or a key.
package jevstub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"

	"github.com/SergeAx/scrutus/internal/core"
)

type Recorded struct {
	// Match is a substring of the comment the state carries.
	Match        string  `json:"match"`
	Accuracy     float64 `json:"accuracy"`
	Usefulness   float64 `json:"usefulness"`
	Overreach    float64 `json:"overreach"`
	CommentedOut float64 `json:"commented_out"`
	Confidence   float64 `json:"confidence"`
}

type Fixtures struct {
	Model       string     `json:"model"`
	InputTokens int        `json:"input_tokens"`
	Default     Recorded   `json:"default"`
	Verdicts    []Recorded `json:"verdicts"`
}

func Load(path string) (Fixtures, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Fixtures{}, err
	}
	var f Fixtures
	err = json.Unmarshal(raw, &f)
	return f, err
}

type Server struct {
	*httptest.Server
	requests atomic.Int64
}

func (s *Server) Requests() int { return int(s.requests.Load()) }

type request struct {
	Model     string                     `json:"model"`
	State     string                     `json:"state"`
	Questions map[string]json.RawMessage `json:"questions"`
}

// New serves POST /v1/systemone, answering every question the request asks
// with the fixture whose Match appears in the state's COMMENT section.
func New(fixtures Fixtures) *Server {
	server := &Server{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			http.NotFound(w, r)
			return
		}
		server.requests.Add(1)

		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		answers := map[string]any{}
		for name := range req.Questions {
			axis, slot, _ := strings.Cut(name, ".")
			recorded := fixtures.match(section(comment(req.State), slot))
			switch axis {
			case "accuracy":
				answers[name] = score(recorded.Accuracy, recorded.Confidence, accuracyLegend)
			case "usefulness":
				answers[name] = score(recorded.Usefulness, recorded.Confidence, usefulnessLegend)
			case "overreach":
				answers[name] = map[string]any{"type": "noul", "noul": recorded.Overreach}
			case "commented_out":
				answers[name] = map[string]any{"type": "noul", "noul": recorded.CommentedOut}
			}
		}

		model := fixtures.Model
		if model == "" {
			model = "jev-1.13.0"
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Typesafe-Request-Id", "stub-request")
		json.NewEncoder(w).Encode(map[string]any{
			"model":   model,
			"usage":   map[string]int{"input_tokens": fixtures.InputTokens, "output_tokens": 0},
			"answers": answers,
		})
	}))
	return server
}

func (f Fixtures) match(commentText string) Recorded {
	for _, recorded := range f.Verdicts {
		if recorded.Match != "" && strings.Contains(commentText, recorded.Match) {
			return recorded
		}
	}
	return f.Default
}

// section is what a question in a grouped request is about: the lines between
// its slot's markers, or for the block's own comment everything outside them.
func section(comment, slot string) string {
	opening, _ := core.SlotMarkers(slot)
	var own, outside []string
	inside := ""
	for line := range strings.SplitSeq(comment, "\n") {
		switch {
		case inside == "" && strings.HasPrefix(line, ">>> "):
			inside = line
		case inside != "" && strings.HasPrefix(line, "<<< "):
			inside = ""
		case inside == "":
			outside = append(outside, line)
		case inside == opening:
			own = append(own, line)
		}
	}
	if slot != "" {
		return strings.Join(own, "\n")
	}
	return strings.Join(outside, "\n")
}

func comment(state string) string {
	_, after, found := strings.Cut(state, "--- COMMENT ---")
	if !found {
		return state
	}
	return strings.TrimSpace(after)
}

var (
	accuracyLegend = map[string]string{
		"0": "Fundamentally wrong",
		"1": "Materially misleading",
		"2": "Roughly correct",
		"3": "Accurate without material errors",
	}
	usefulnessLegend = map[string]string{
		"0": "No information beyond the code",
		"1": "Marginal",
		"2": "Useful",
		"3": "Essential context",
	}
)

func score(value, confidence float64, legend map[string]string) map[string]any {
	if confidence == 0 {
		confidence = 0.9
	}
	probabilities := map[string]float64{"0": 0, "1": 0, "2": 0, "3": 0}
	probabilities[key(value)] = 1
	return map[string]any{
		"type":          "score",
		"score":         value,
		"confidence":    confidence,
		"legend":        legend,
		"probabilities": probabilities,
	}
}

func key(value float64) string {
	switch {
	case value < 0.5:
		return "0"
	case value < 1.5:
		return "1"
	case value < 2.5:
		return "2"
	default:
		return "3"
	}
}
