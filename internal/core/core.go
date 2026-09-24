// Package core holds the types every stage of the pipeline passes along.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

type Kind string

const (
	KindDoc        Kind = "doc"
	KindInline     Kind = "inline"
	KindTrailing   Kind = "trailing"
	KindAnnotation Kind = "annotation"
)

type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type Finding struct {
	ID          string
	File        string
	Comment     Span
	Code        Span
	Kind        Kind
	CommentText string
	CodeText    string
	Context     string
	Line        int
	Column      int

	// Block groups findings that share one request state; Slot names a
	// finding inside it ("" for the block's own comment, "a1" upwards for
	// annotation lines).
	Block string
	Slot  string
	// BlockText is the whole block with every slot between its markers, the
	// comment a grouped request shows; empty when the block has no slots.
	BlockText string

	// Protected marks a comment the keep_* settings shield from deletion: an
	// exported Go doc comment, a PHP docblock or a Python docstring.
	Protected bool
	// Required marks a comment the code cannot parse without, such as a
	// docstring that is its body's only statement.
	Required bool
}

// SlotMarkers are the lines around a slot in BlockText, which the questions
// about that slot name.
func SlotMarkers(slot string) (opening, closing string) {
	slot = strings.ToUpper(slot)
	return ">>> " + slot, "<<< " + slot
}

// NewID hashes what a verdict actually depends on, so moving code between
// files neither invalidates the cache nor resurrects a baselined finding.
func NewID(commentText, codeText string, rubricVersion int, model string) string {
	h := sha256.New()
	for _, part := range []string{commentText, codeText, strconv.Itoa(rubricVersion), model} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

type Axis struct {
	Score      float64 `json:"score"`
	Pct        int     `json:"pct"`
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
}

type Noul struct {
	Prob float64 `json:"prob"`
}

type Usage struct {
	InputTokens int     `json:"input_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

type Verdict struct {
	FindingID  string
	Accuracy   Axis
	Usefulness Axis
	Overreach  Noul
	Usage      Usage
	Model      string
}

type Action string

const (
	ActionKeep   Action = "keep"
	ActionDelete Action = "delete"
	ActionFlag   Action = "flag"
)

type Severity string

const (
	SeverityNone    Severity = ""
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Rule ids are a public contract: SARIF, baselines and --fail-on consume them.
const (
	RuleWrongComment        = "wrong-comment"
	RuleUselessComment      = "useless-comment"
	RuleRedundantAnnotation = "redundant-annotation"
	RuleWeakComment         = "weak-comment"
	RuleWideScope           = "wide-scope"
	RuleLowConfidence       = "low-confidence"
	RuleFixAborted          = "fix-aborted"
)

type Result struct {
	Finding  Finding
	Verdict  Verdict
	Action   Action
	Severity Severity
	Rule     string
}

type RunInfo struct {
	Mode         string
	Scope        string
	Base         string
	Files        int
	Comments     int
	Cached       int
	Assessed     int
	Requests     int
	Suppressed   int
	Baselined    int
	Deleted      int
	InputTokens  int
	CostUSD      float64
	UsageUnknown bool
	Incomplete   bool
	DurationMS   int64
	Tool         string
	Version      string
	RubricVer    int
	Model        string
}

func SeverityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	}
	return 0
}
