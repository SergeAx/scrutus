// Package jev scores findings with TypeSafe's System One through the SDK.
package jev

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
	typesafe "serge.ax/go/typesafe-sdk-go"

	"github.com/SergeAx/scrutus/internal/assess"
	"github.com/SergeAx/scrutus/internal/core"
)

// USDPerInputToken is Jev's Score price: $0.042 per million input tokens,
// output free.
const USDPerInputToken = 0.042 / 1e6

type Assessor struct {
	client      *typesafe.Client
	rubric      assess.Rubric
	concurrency int
	budgetUSD   float64

	requests atomic.Int64
	spent    struct {
		sync.Mutex
		usd float64
	}
}

func New(client *typesafe.Client, rubric assess.Rubric, concurrency int, budgetUSD float64) *Assessor {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Assessor{client: client, rubric: rubric, concurrency: concurrency, budgetUSD: budgetUSD}
}

func (a *Assessor) Requests() int { return int(a.requests.Load()) }

// ErrBudget is returned when actual spend crosses the run's budget.
var ErrBudget = errors.New("budget exceeded")

func (a *Assessor) Assess(ctx context.Context, findings []core.Finding) ([]core.Verdict, error) {
	blocks := group(findings)
	verdicts := make([][]core.Verdict, len(blocks))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(a.concurrency)
	for i, blockFindings := range blocks {
		g.Go(func() error {
			got, err := a.assessBlock(ctx, blockFindings)
			if err != nil {
				return err
			}
			verdicts[i] = got
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	var all []core.Verdict
	for _, got := range verdicts {
		all = append(all, got...)
	}
	return all, nil
}

// group collects findings that share a state, so a docblock and its annotation
// lines cost one state instead of one each.
func group(findings []core.Finding) [][]core.Finding {
	order := []string{}
	byBlock := map[string][]core.Finding{}
	for _, f := range findings {
		key := f.Block
		if key == "" {
			key = f.ID
		}
		if _, seen := byBlock[key]; !seen {
			order = append(order, key)
		}
		byBlock[key] = append(byBlock[key], f)
	}
	blocks := make([][]core.Finding, 0, len(order))
	for _, key := range order {
		blocks = append(blocks, byBlock[key])
	}
	return blocks
}

func (a *Assessor) assessBlock(ctx context.Context, findings []core.Finding) ([]core.Verdict, error) {
	questions := typesafe.Questions{}
	for _, f := range findings {
		suffix := ""
		if f.Slot != "" {
			suffix = "." + f.Slot
		}
		questions["accuracy"+suffix] = typesafe.Score{
			Instructions: a.instructions(a.rubric.Accuracy.Instructions, f.Slot),
			Criteria:     contents(a.rubric.Accuracy.Criteria),
		}
		questions["usefulness"+suffix] = typesafe.Score{
			Instructions: a.instructions(a.rubric.Usefulness.Instructions, f.Slot),
			Criteria:     contents(a.rubric.Usefulness.Criteria),
		}
		questions["overreach"+suffix] = typesafe.Noul{
			Instructions: a.instructions(a.rubric.Overreach.Instructions, f.Slot),
		}
	}

	a.requests.Add(1)
	resp, err := a.client.SystemOne(ctx, assess.State(findings[0]), questions)
	if err != nil {
		return nil, err
	}

	scores, nouls := resp.Scores(), resp.Nouls()
	perFinding := core.Usage{
		InputTokens: resp.Usage.InputTokens / len(findings),
	}
	perFinding.CostUSD = float64(perFinding.InputTokens) * USDPerInputToken

	out := make([]core.Verdict, 0, len(findings))
	for _, f := range findings {
		suffix := ""
		if f.Slot != "" {
			suffix = "." + f.Slot
		}
		accuracy, ok := scores["accuracy"+suffix]
		if !ok || accuracy == nil {
			return nil, missing(f, "accuracy"+suffix, resp.RequestID)
		}
		usefulness, ok := scores["usefulness"+suffix]
		if !ok || usefulness == nil {
			return nil, missing(f, "usefulness"+suffix, resp.RequestID)
		}
		overreach, ok := nouls["overreach"+suffix]
		if !ok || overreach == nil {
			return nil, missing(f, "overreach"+suffix, resp.RequestID)
		}
		out = append(out, core.Verdict{
			FindingID:  f.ID,
			Accuracy:   toAxis(accuracy, assess.Levels),
			Usefulness: toAxis(usefulness, assess.Levels),
			Overreach:  core.Overreach{Prob: overreach.Noul},
			Usage:      perFinding,
			Model:      resp.Model,
		})
	}

	if err := a.charge(float64(len(findings)) * perFinding.CostUSD); err != nil {
		return out, err
	}
	return out, nil
}

// instructions names the marked slot for an annotation question, so one
// request can ask about several marked lines in the same state.
func (a *Assessor) instructions(base, slot string) string {
	if slot == "" {
		return base
	}
	return fmt.Sprintf("%s\nAnswer only about the line marked %s in the COMMENT block.",
		base, slotMarker(slot))
}

func slotMarker(slot string) string { return ">>> " + upper(slot) }

func upper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}

func missing(f core.Finding, question, requestID string) error {
	return fmt.Errorf("%s:%d: the API answered no %q question (request_id=%s)",
		f.File, f.Line, question, requestID)
}

func (a *Assessor) charge(usd float64) error {
	a.spent.Lock()
	defer a.spent.Unlock()
	a.spent.usd += usd
	if a.budgetUSD > 0 && a.spent.usd > a.budgetUSD {
		return fmt.Errorf("%w: spent $%.4f of $%.4f", ErrBudget, a.spent.usd, a.budgetUSD)
	}
	return nil
}

func toAxis(a *typesafe.ScoreAnswer, levels int) core.Axis {
	pct := int(math.Round(100 * a.Score / float64(levels-1)))
	label, _ := a.Legend[int(math.Round(a.Score))].(string)
	return core.Axis{Score: a.Score, Pct: pct, Label: label, Confidence: a.Confidence}
}

func contents(criteria []string) []typesafe.Content {
	out := make([]typesafe.Content, 0, len(criteria))
	for _, c := range criteria {
		out = append(out, c)
	}
	return out
}
