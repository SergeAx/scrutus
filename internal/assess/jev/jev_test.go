package jev_test

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"testing"

	typesafe "serge.ax/go/typesafe-sdk-go"

	"github.com/SergeAx/scrutus/internal/assess"
	"github.com/SergeAx/scrutus/internal/assess/jev"
	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/jevstub"
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
				findings = append(findings, core.Finding{ID: "prose", Block: "b", BlockText: comment})
			}
			for i := range b.tags {
				slot := "a" + strconv.Itoa(i+1)
				findings = append(findings, core.Finding{ID: slot, Block: "b", BlockText: comment, Slot: slot})
			}
		}

		got := jev.EstimateTokens(findings, rubric)
		if miss := math.Abs(float64(got-b.tokens)) / float64(b.tokens); miss > 0.1 {
			t.Errorf("%+v: estimated %d tokens, %.0f%% off", b, got, 100*miss)
		}
	}
}

func TestALargeBlockIsSplitAcrossRequests(t *testing.T) {
	server := jevstub.New(jevstub.Fixtures{})
	t.Cleanup(server.Close)
	client, err := typesafe.New(
		typesafe.WithAPIKey("test-key"),
		typesafe.WithBaseURL(server.URL),
		typesafe.WithLogger(slog.New(slog.DiscardHandler)),
	)
	if err != nil {
		t.Fatal(err)
	}
	rubric, err := assess.LoadRubric("default")
	if err != nil {
		t.Fatal(err)
	}

	const tags = 200
	var findings []core.Finding
	for i := range tags {
		slot := "a" + strconv.Itoa(i+1)
		findings = append(findings, core.Finding{ID: slot, Block: "b", BlockText: "/** @property int $id */", Slot: slot})
	}
	verdicts, err := jev.New(client, rubric, 4, 0).Assess(context.Background(), findings)
	if err != nil {
		t.Fatalf("assess: %v", err)
	}

	if len(verdicts) != tags {
		t.Errorf("%d verdicts, want %d", len(verdicts), tags)
	}
	if server.Requests() < 2 {
		t.Errorf("%d request for %d tags, want the block split", server.Requests(), tags)
	}
}
