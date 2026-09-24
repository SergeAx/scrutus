// Package jev scores findings with TypeSafe's System One through the SDK.
package jev

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
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
	batches := requests(findings, a.rubric)
	verdicts := make([][]core.Verdict, len(batches))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(a.concurrency)
	for i, batch := range batches {
		g.Go(func() error {
			got, err := a.assessBlock(ctx, batch.findings)
			verdicts[i] = got
			return err
		})
	}
	err := g.Wait()

	var all []core.Verdict
	for _, got := range verdicts {
		all = append(all, got...)
	}
	return all, err
}

// Jev bills about a token per 3.6 bytes of state and question text, plus about
// 250 a request for its own framing: fitted on the 5 800 requests of one run,
// to within 2% a request on average. maxRequestTokens stays well inside its
// input limit, which a model docblock of 188 tags came within 3% of.
const (
	bytesPerToken    = 3.6
	tokensPerRequest = 250
	maxRequestTokens = 32_000
)

func tokens(size int) int { return int(float64(size)/bytesPerToken) + tokensPerRequest }

// EstimateTokens predicts the input tokens Assess will be billed for the
// findings, so a run can refuse before spending.
func EstimateTokens(findings []core.Finding, rubric assess.Rubric) int {
	total := 0
	for _, r := range requests(findings, rubric) {
		total += tokens(r.size)
	}
	return total
}

// request is a batch of findings sent as one, and the bytes of state and
// question text Jev bills it by.
type request struct {
	findings []core.Finding
	size     int
	asked    map[string]bool
}

// requests lays findings out as Assess sends them: a request per block, and a
// block too large for one split across several that each repeat its state.
func requests(findings []core.Finding, rubric assess.Rubric) []request {
	var out []request
	for _, block := range group(findings) {
		state := len(assess.State(block[0]))
		r := request{size: state, asked: map[string]bool{}}
		for _, f := range block {
			if len(r.findings) > 0 && tokens(r.size+r.growth(rubric, f)) > maxRequestTokens {
				out = append(out, r)
				r = request{size: state, asked: map[string]bool{}}
			}
			r.size += r.growth(rubric, f)
			for _, q := range questionsFor(rubric, f) {
				r.asked[q.name] = true
			}
			r.findings = append(r.findings, f)
		}
		out = append(out, r)
	}
	return out
}

// growth is the question text f adds; findings sharing an ID share questions.
func (r request) growth(rubric assess.Rubric, f core.Finding) int {
	size := 0
	for _, q := range questionsFor(rubric, f) {
		if !r.asked[q.name] {
			size += len(q.instructions) + len(strings.Join(q.criteria, ""))
		}
	}
	return size
}

// question is one of the three a finding is asked; criteria is nil for the
// overreach probability, which has no levels.
type question struct {
	name         string
	instructions string
	criteria     []string
}

func questionsFor(rubric assess.Rubric, f core.Finding) []question {
	suffix := ""
	if f.Slot != "" {
		suffix = "." + f.Slot
	}
	return []question{
		{"accuracy" + suffix, instructions(rubric.Accuracy.Instructions, f), rubric.Accuracy.Criteria},
		{"usefulness" + suffix, instructions(rubric.Usefulness.Instructions, f), rubric.Usefulness.Criteria},
		{"overreach" + suffix, instructions(rubric.Overreach.Instructions, f), nil},
	}
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
		for _, q := range questionsFor(a.rubric, f) {
			if q.criteria == nil {
				questions[q.name] = typesafe.Noul{Instructions: q.instructions}
			} else {
				questions[q.name] = typesafe.Score{Instructions: q.instructions, Criteria: contents(q.criteria)}
			}
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

// instructions narrows a question in a grouped request to its own lines of the
// block, so one state serves the docblock's prose and every tag in it.
func instructions(base string, f core.Finding) string {
	switch {
	case f.Slot != "":
		opening, closing := core.SlotMarkers(f.Slot)
		return fmt.Sprintf("%s\nAnswer only about the lines between %s and %s in the COMMENT block.",
			base, opening, closing)
	case f.BlockText != "":
		return base + "\nAnswer only about the COMMENT text outside the marked lines."
	}
	return base
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
