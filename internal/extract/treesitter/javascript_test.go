//go:build cgo

package treesitter_test

import (
	"strings"
	"testing"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
	_ "github.com/SergeAx/scrutus/internal/extract/treesitter"
)

const tsSrc = `/// <reference types="node" />
import { settle } from './ledger';

/**
 * Settles the invoice against the ledger.
 *
 * @param id the invoice number
 * @returns the ledger reference
 */
export function settleInvoice(id: number): string {
  // Load the invoice, then guard the already-paid case.
  const invoice = load(id);
  const paid = invoice.paid;

  // Upper-case the reference
  // because the ledger compares it byte for byte.
  const reference = invoice.reference.toUpperCase();

  return settle(reference); // the ledger reference
}

// Retry budget shared by every client.
export const retries = 3;

const config = {
  // Seconds before the gateway gives up.
  timeout: 30,
};

class Gateway {
  /** Opens the socket lazily. */
  @memoize()
  connect(): void {
    open()
      // the handshake needs a second round trip
      .then(handshake);
  }
}

interface Invoice {
  // Cents, never floats.
  amount: number;
}
`

func extractFrom(t *testing.T, file, src string) map[string]core.Finding {
	t.Helper()

	got, err := extract.Extract(file, []byte(src), []string{"javascript", "typescript"}, extract.Options{ContextLines: 20, MaxCodeLines: 40})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	byComment := map[string]core.Finding{}
	for _, f := range got {
		byComment[strings.TrimSpace(f.CommentText)] = f
	}
	return byComment
}

func TestTypeScriptPairingRules(t *testing.T) {
	all := extractFrom(t, "invoices.ts", tsSrc)

	cases := []struct {
		comment string
		kind    core.Kind
		codeHas string
		codeNot string
	}{
		{"// Load the invoice, then guard the already-paid case.", core.KindInline, "const paid = invoice.paid;", "toUpperCase"},
		{"// Upper-case the reference\n  // because the ledger compares it byte for byte.", core.KindInline, "toUpperCase", "return"},
		{"// the ledger reference", core.KindTrailing, "return settle(reference);", "toUpperCase"},
		{"// Retry budget shared by every client.", core.KindDoc, "export const retries = 3;", ""},
		{"// Seconds before the gateway gives up.", core.KindInline, "timeout: 30", "const config"},
		{"/** Opens the socket lazily. */", core.KindDoc, "@memoize()\n  connect(): void {", ""},
		{"// the handshake needs a second round trip", core.KindInline, ".then(handshake);", "class Gateway"},
		{"// Cents, never floats.", core.KindDoc, "amount: number", ""},
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
		if tc.codeNot != "" && strings.Contains(f.CodeText, tc.codeNot) {
			t.Errorf("%q: code %q reaches %q", tc.comment, f.CodeText, tc.codeNot)
		}
	}
	if len(all) != len(cases)+3 {
		for comment := range all {
			t.Logf("extracted %q", comment)
		}
		t.Errorf("%d findings, want %d", len(all), len(cases)+3)
	}
}

func TestJSDocSplitsIntoProseAndTags(t *testing.T) {
	var prose core.Finding
	var tags []core.Finding
	for _, f := range extractFrom(t, "invoices.ts", tsSrc) {
		switch {
		case f.Kind == core.KindAnnotation:
			tags = append(tags, f)
		case strings.Contains(f.CommentText, "Settles the invoice"):
			prose = f
		}
	}

	if prose.Kind != core.KindDoc || !strings.Contains(prose.CodeText, "export function settleInvoice") {
		t.Errorf("prose: kind %s, code %q", prose.Kind, prose.CodeText)
	}
	if strings.Contains(tsSrc[prose.Comment.Start:prose.Comment.End], "@param") {
		t.Error("the prose span swallowed the tags")
	}
	if len(tags) != 2 {
		t.Fatalf("%d tags, want @param and @returns", len(tags))
	}
	for _, tag := range tags {
		if tag.Block == "" || tag.Block != prose.Block {
			t.Errorf("%q does not share the prose's block", tag.CommentText)
		}
		if got := tsSrc[tag.Comment.Start:tag.Comment.End]; got != tag.CommentText {
			t.Errorf("tag span reads %q, want %q", got, tag.CommentText)
		}
	}
}

func TestTripleSlashDirectiveIsNotAComment(t *testing.T) {
	for comment := range extractFrom(t, "invoices.ts", tsSrc) {
		if strings.Contains(comment, "<reference") {
			t.Errorf("extracted the compiler directive %q", comment)
		}
	}
}

func TestLocalDeclarationHeadsAParagraph(t *testing.T) {
	const src = `function total(items) {
  // Sum in cents to dodge float drift.
  const cents = items.map(toCents);
  return cents.reduce(add, 0);
}
`
	f, ok := extractFrom(t, "total.js", src)["// Sum in cents to dodge float drift."]
	if !ok {
		t.Fatal("comment not extracted")
	}
	if f.Kind != core.KindInline || !strings.Contains(f.CodeText, "return cents.reduce") {
		t.Errorf("kind %s, code %q; want the inline paragraph", f.Kind, f.CodeText)
	}
	if !strings.Contains(f.Context, ">>> CODE") || !strings.Contains(f.Context, "function total(items)") {
		t.Errorf("context:\n%s", f.Context)
	}
	if strings.Contains(f.Context, "dodge") {
		t.Errorf("context kept the comment:\n%s", f.Context)
	}
}

func TestJSXParsesInTSXFiles(t *testing.T) {
	const src = `export const Badge = () => (
  // Rendered inside a flex row.
  <span className="badge">{label}</span>
);
`
	if _, ok := extractFrom(t, "badge.tsx", src)["// Rendered inside a flex row."]; !ok {
		t.Error("the comment in a .tsx file was not extracted")
	}
}

func TestCRLFSourcesKeepExactSpans(t *testing.T) {
	src := strings.ReplaceAll("const a = 1; // the first\n// Heads the second.\nconst b = 2;\n", "\n", "\r\n")
	all := extractFrom(t, "crlf.js", src)
	for _, comment := range []string{"// the first", "// Heads the second."} {
		f, ok := all[comment]
		if !ok {
			t.Fatalf("%q not extracted from %q", comment, src)
		}
		if got := src[f.Comment.Start:f.Comment.End]; got != comment {
			t.Errorf("span reads %q, want %q", got, comment)
		}
	}
}

func TestSyntaxErrorIsAnError(t *testing.T) {
	_, err := extract.Extract("broken.js", []byte("function f( {\n"), []string{"javascript"}, extract.Options{})
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("err = %v, want a syntax error naming line 1", err)
	}
}
