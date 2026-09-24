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
	{state: 597, prose: 1, copies: 1, tokens: 755},
	{state: 403, tags: 1, copies: 1, tokens: 719},
	{state: 348, prose: 1, tags: 1, copies: 1, tokens: 1074},
	{state: 678, tags: 2, copies: 1, tokens: 1144},
	{state: 655, tags: 3, copies: 1, tokens: 1494},
	{state: 526, tags: 4, copies: 1, tokens: 1792},
	{state: 454, prose: 1, copies: 2, tokens: 708},
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
				findings = append(findings, core.Finding{ID: slot, Kind: core.KindAnnotation, Block: "b", BlockText: comment, Slot: slot})
			}
		}

		got := jev.EstimateTokens(findings, rubric)
		if miss := math.Abs(float64(got-b.tokens)) / float64(b.tokens); miss > 0.1 {
			t.Errorf("%+v: estimated %d tokens, %.0f%% off", b, got, 100*miss)
		}
	}
}

func stubbed(t *testing.T, fixtures jevstub.Fixtures) (*jev.Assessor, *jevstub.Server) {
	t.Helper()

	server := jevstub.New(fixtures)
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
	return jev.New(client, rubric, 4, 0), server
}

func TestALargeBlockIsSplitAcrossRequests(t *testing.T) {
	assessor, server := stubbed(t, jevstub.Fixtures{})

	const tags = 200
	var findings []core.Finding
	for i := range tags {
		slot := "a" + strconv.Itoa(i+1)
		findings = append(findings, core.Finding{ID: slot, Kind: core.KindAnnotation, Block: "b", BlockText: "/** @property int $id */", Slot: slot})
	}
	verdicts, err := assessor.Assess(context.Background(), findings)
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

func TestOnlyProseIsAskedWhetherItIsCode(t *testing.T) {
	assessor, _ := stubbed(t, jevstub.Fixtures{Default: jevstub.Recorded{CommentedOut: 0.9}})

	block := `/**
 * Finds the order by its ID.
>>> A1
 * @param int $id
<<< A1
 */`
	verdicts, err := assessor.Assess(context.Background(), []core.Finding{
		{ID: "prose", Kind: core.KindDoc, Block: "b", BlockText: block},
		{ID: "tag", Kind: core.KindAnnotation, Block: "b", BlockText: block, Slot: "a1"},
	})
	if err != nil {
		t.Fatalf("assess: %v", err)
	}

	got := map[string]float64{}
	for _, v := range verdicts {
		got[v.FindingID] = v.CommentedOut.Prob
	}
	if got["prose"] != 0.9 || got["tag"] != 0 {
		t.Errorf("commented-out probabilities %v, want the prose answered and the tag never asked", got)
	}
}
