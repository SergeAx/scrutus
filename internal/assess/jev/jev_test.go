package jev_test

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/SergeAx/scrutus/internal/assess"
	"github.com/SergeAx/scrutus/internal/assess/jev"
	"github.com/SergeAx/scrutus/internal/core"
)

// Requests from a run over a PHP codebase: the bytes of their state, what
// they asked about, and the input tokens Jev billed for them.
var billed = []struct {
	state, prose, tags, copies, tokens int
}{
	{state: 558, prose: 1, copies: 1, tokens: 686},
	{state: 372, tags: 1, copies: 1, tokens: 685},
	{state: 313, prose: 1, tags: 1, copies: 1, tokens: 958},
	{state: 607, tags: 2, copies: 1, tokens: 1080},
	{state: 528, tags: 3, copies: 1, tokens: 1365},
	{state: 348, tags: 4, copies: 1, tokens: 1680},
	{state: 595, prose: 1, copies: 5, tokens: 675},
}

func TestEstimateTracksWhatJevBilled(t *testing.T) {
	rubric, err := assess.LoadRubric("default")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range billed {
		comment := strings.Repeat("x", b.state-len(assess.State(core.Finding{})))
		var findings []core.Finding
		if b.tags == 0 {
			for range b.copies {
				findings = append(findings, core.Finding{ID: "same", CommentText: comment})
			}
		} else {
			if b.prose > 0 {
				findings = append(findings, core.Finding{ID: "prose", Block: "b", CommentText: comment})
			}
			for i := range b.tags {
				slot := "a" + strconv.Itoa(i+1)
				findings = append(findings, core.Finding{ID: slot, Block: "b", CommentText: comment, Slot: slot})
			}
		}

		got := jev.EstimateTokens(findings, rubric)
		if miss := math.Abs(float64(got-b.tokens)) / float64(b.tokens); miss > 0.1 {
			t.Errorf("%+v: estimated %d tokens, %.0f%% off", b, got, 100*miss)
		}
	}
}
