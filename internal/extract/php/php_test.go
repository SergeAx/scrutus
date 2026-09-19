package php_test

import (
	"strings"
	"testing"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
	_ "github.com/SergeAx/scrutus/internal/extract/php"
)

const src = `<?php

namespace App\Services;

class Invoices
{
    /**
     * Settles the invoice against the ledger.
     *
     * @param int $id
     * @return string
     */
    public function settle(int $id): string
    {
        // Load the invoice, then guard the already-paid case.
        $invoice = Invoice::findOrFail($id);
        $paid = $invoice->paid;

        # Hash comment heading one statement.
        $reference = strtoupper($invoice->reference);

        return $reference; // the ledger reference
    }

    /** @var Client */
    private $client;
}
`

func findings(t *testing.T) map[string]core.Finding {
	t.Helper()

	got, err := extract.Extract("Invoices.php", []byte(src), []string{"php"}, extract.Options{ContextLines: 20, MaxCodeLines: 40})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	byComment := map[string]core.Finding{}
	for _, f := range got {
		byComment[strings.TrimSpace(f.CommentText)] = f
	}
	return byComment
}

func TestDocblockSplitsIntoProseAndAnnotations(t *testing.T) {
	all := findings(t)

	var prose core.Finding
	for comment, f := range all {
		if f.Kind == core.KindDoc && strings.Contains(comment, "Settles the invoice") {
			prose = f
		}
	}
	if prose.CommentText == "" {
		for comment := range all {
			t.Logf("extracted %q", comment)
		}
		t.Fatal("the docblock's prose was not extracted")
	}
	if got := src[prose.Comment.Start:prose.Comment.End]; strings.Contains(got, "@param") {
		t.Errorf("the prose span swallowed the tags: %q", got)
	}
	if prose.Kind != core.KindDoc {
		t.Errorf("prose kind = %s, want doc", prose.Kind)
	}
	if !strings.Contains(prose.CodeText, "public function settle(int $id): string") {
		t.Errorf("prose paired with %q", prose.CodeText)
	}
	if !prose.Protected {
		t.Error("a docblock on a declaration should be protected")
	}

	var tags []core.Finding
	for _, f := range all {
		if f.Kind == core.KindAnnotation {
			tags = append(tags, f)
		}
	}
	if len(tags) != 3 {
		t.Fatalf("%d annotation findings, want 3 (@param, @return, @var)", len(tags))
	}
	for _, tag := range tags {
		if tag.Slot == "" {
			t.Errorf("%q carries no slot", tag.CommentText)
		}
		if tag.Block == "" {
			t.Errorf("%q carries no block, so it would not share a request", tag.CommentText)
		}
		if tag.Protected {
			t.Errorf("%q is protected; only prose doc comments are", tag.CommentText)
		}
	}
}

// Annotations from one docblock share a state, which is what keeps the split
// from multiplying the token bill.
func TestAnnotationsShareTheirDocblockBlock(t *testing.T) {
	blocks := map[string]int{}
	for _, f := range findings(t) {
		if f.Block != "" {
			blocks[f.Block]++
		}
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %v, want one per docblock", blocks)
	}
	for block, count := range blocks {
		if count < 2 && !strings.Contains(block, "Invoices.php") {
			t.Errorf("block %q holds %d findings", block, count)
		}
	}
}

func TestLineAndTrailingComments(t *testing.T) {
	all := findings(t)

	cases := []struct {
		comment string
		kind    core.Kind
		codeHas string
	}{
		{"// Load the invoice, then guard the already-paid case.", core.KindInline, "$invoice = Invoice::findOrFail($id);"},
		{"# Hash comment heading one statement.", core.KindInline, "$reference = strtoupper($invoice->reference);"},
		{"// the ledger reference", core.KindTrailing, "return $reference;"},
	}
	for _, tc := range cases {
		f, ok := all[tc.comment]
		if !ok {
			t.Errorf("%q was not extracted", tc.comment)
			continue
		}
		if f.Kind != tc.kind {
			t.Errorf("%q: kind %s, want %s", tc.comment, f.Kind, tc.kind)
		}
		if !strings.Contains(f.CodeText, tc.codeHas) {
			t.Errorf("%q: code %q lacks %q", tc.comment, f.CodeText, tc.codeHas)
		}
	}
}

// An inline `/** @var Foo $bar */` types a local variable, so it pairs with the
// statement below it, never with the next method.
func TestInlineVarDocblockPairsWithTheStatement(t *testing.T) {
	const body = `<?php

class Repo
{
    public function find(int $id)
    {
        /** @var Invoice $invoice */
        $invoice = Invoice::find($id);

        return $invoice;
    }

    public function other(): void
    {
    }
}
`
	got, err := extract.Extract("Repo.php", []byte(body), []string{"php"}, extract.Options{ContextLines: 20, MaxCodeLines: 40})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	var tag core.Finding
	for _, f := range got {
		if f.Kind == core.KindAnnotation {
			tag = f
		}
	}
	if tag.CommentText == "" {
		t.Fatal("the inline docblock produced no annotation finding")
	}
	if tag.CommentText != "@var Invoice $invoice" {
		t.Errorf("comment = %q, want the tag without its delimiters", tag.CommentText)
	}
	if !strings.Contains(tag.CodeText, "$invoice = Invoice::find($id);") {
		t.Errorf("paired with %q, want the statement below it", tag.CodeText)
	}
	if strings.Contains(tag.CodeText, "function other") {
		t.Errorf("paired with a later declaration: %q", tag.CodeText)
	}
}

func TestParagraphStopsAtBlankLine(t *testing.T) {
	f, ok := findings(t)["// Load the invoice, then guard the already-paid case."]
	if !ok {
		t.Fatal("comment not extracted")
	}
	if !strings.Contains(f.CodeText, "$paid = $invoice->paid;") {
		t.Errorf("paragraph missed the second statement: %q", f.CodeText)
	}
	if strings.Contains(f.CodeText, "strtoupper") {
		t.Errorf("paragraph ran past the blank line: %q", f.CodeText)
	}
}

func TestSpansPointAtTheComment(t *testing.T) {
	for comment, f := range findings(t) {
		got := strings.TrimSpace(src[f.Comment.Start:f.Comment.End])
		if f.Kind == core.KindAnnotation {
			if !strings.HasPrefix(got, "@") {
				t.Errorf("annotation span reads %q", got)
			}
			continue
		}
		if f.Kind == core.KindDoc {
			if !strings.HasPrefix(got, "/**") {
				t.Errorf("doc span reads %q", got)
			}
			continue
		}
		if got != comment {
			t.Errorf("span %d-%d reads %q, want %q", f.Comment.Start, f.Comment.End, got, comment)
		}
	}
}
