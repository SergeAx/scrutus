//go:build cgo

package treesitter_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
	"github.com/SergeAx/scrutus/internal/fix"
)

const pySrc = `#!/usr/bin/env python3
"""Settle invoices against the ledger."""

import ledger

RETRIES = {
    # Seconds before the gateway gives up.
    "timeout": 30,
}


@cached
def settle(invoice_id):
    """Return the ledger reference for a settled invoice."""
    # Load the invoice, then guard the already-paid case.
    invoice = ledger.load(invoice_id)
    paid = invoice.paid

    return ledger.settle(invoice)  # the ledger reference


class Gateway:
    """Talks to the payment gateway."""

    def connect(self):
        # Open lazily: the handshake is slow.
        # It needs a second round trip.
        self.socket = open_socket()
        # Close on exit.
# Module-level trailer.


def noop():
    """Only a docstring."""


def formatted():
    f"""Not a docstring: {__name__}"""
`

func pyFindings(t *testing.T) map[string]core.Finding {
	t.Helper()
	return extractFrom(t, "invoices.py", pySrc)
}

func TestPythonPairingRules(t *testing.T) {
	all := pyFindings(t)

	cases := []struct {
		comment string
		kind    core.Kind
		codeHas string
		codeNot string
	}{
		{"# Seconds before the gateway gives up.", core.KindInline, `"timeout": 30`, "RETRIES"},
		{"# Load the invoice, then guard the already-paid case.", core.KindInline, "paid = invoice.paid", "return"},
		{"# the ledger reference", core.KindTrailing, "return ledger.settle(invoice)", "paid"},
		{"# Open lazily: the handshake is slow.\n        # It needs a second round trip.", core.KindInline, "self.socket = open_socket()", "def connect"},
		{"# Close on exit.", core.KindTrailing, "self.socket = open_socket()", ""},
		{"# Module-level trailer.", core.KindInline, "def noop():", ""},
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
}

func TestDocstringsPairWithTheirDeclaration(t *testing.T) {
	all := pyFindings(t)

	cases := []struct {
		text     string
		codeHas  string
		required bool
	}{
		{"Settle invoices against the ledger.", "import ledger", false},
		{"Return the ledger reference for a settled invoice.", "@cached\ndef settle(invoice_id):", false},
		{"Talks to the payment gateway.", "class Gateway:", false},
		{"Only a docstring.", "def noop():", true},
	}
	for _, tc := range cases {
		f, ok := all[tc.text]
		if !ok {
			t.Errorf("docstring %q was not extracted", tc.text)
			continue
		}
		if f.Kind != core.KindDoc || !f.Protected || f.Required != tc.required {
			t.Errorf("%q: kind %s, protected %v, required %v", tc.text, f.Kind, f.Protected, f.Required)
		}
		if !strings.Contains(f.CodeText, tc.codeHas) {
			t.Errorf("%q: code %q lacks %q", tc.text, f.CodeText, tc.codeHas)
		}
		if strings.Contains(f.CodeText, tc.text) {
			t.Errorf("%q: the code still carries the docstring: %q", tc.text, f.CodeText)
		}
		if got := pySrc[f.Comment.Start:f.Comment.End]; !strings.HasPrefix(got, `"""`) || !strings.HasSuffix(got, `"""`) {
			t.Errorf("%q: span reads %q, want the quoted statement", tc.text, got)
		}
	}
	for text := range all {
		if strings.Contains(text, "Not a docstring") {
			t.Errorf("an f-string was taken for a docstring")
		}
	}
}

func TestDocstringsStayOutOfOtherContexts(t *testing.T) {
	f := pyFindings(t)["# Load the invoice, then guard the already-paid case."]
	if !strings.Contains(f.Context, "def settle") || strings.Contains(f.Context, "Return the ledger reference") {
		t.Errorf("context:\n%s", f.Context)
	}
}

func TestDeletingADocstringLeavesAParseableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invoices.py")
	if err := os.WriteFile(path, []byte(pySrc), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := pyFindings(t)["Talks to the payment gateway."]
	doc.File = path

	applied, err := fix.Apply([]core.Result{{Action: core.ActionDelete, Finding: doc}},
		map[string][]byte{path: []byte(pySrc)}, fix.Options{Languages: []string{"python"}})
	if err != nil || len(applied) != 1 || applied[0].Err != nil {
		t.Fatalf("apply: %v, %+v", err, applied)
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(pySrc, "    \"\"\"Talks to the payment gateway.\"\"\"\n", "", 1); string(edited) != want {
		t.Errorf("edited file:\n%s", edited)
	}
	if _, err := extract.Extract(path, edited, []string{"python"}, extract.Options{}); err != nil {
		t.Errorf("the edited file does not parse: %v", err)
	}
}

func TestDedentEndsACommentGroup(t *testing.T) {
	const src = "def f():\n    x = 1\n    # end of f\n# about g\ndef g():\n    pass\n"
	all := extractFrom(t, "dedent.py", src)
	if _, ok := all["# end of f"]; !ok {
		t.Errorf("the dedented comment merged into its neighbour: %v", keys(all))
	}
	if f, ok := all["# about g"]; !ok || !strings.Contains(f.CodeText, "def g():") {
		t.Errorf("# about g: %+v", f)
	}
}

func keys(m map[string]core.Finding) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPythonCommentAboveAClausePairsWithIt(t *testing.T) {
	got, err := extract.Extract("route.py", []byte(`def route(visible, menu):
    if visible:
        close()
    # Opens the menu when it is hidden.
    elif menu:
        open_menu()
    # Falls back to the default layout.
    else:
        reset()

    try:
        load()
    # A missing cache is not an error.
    except CacheMiss:
        warm()
    # Releases the lock either way.
    finally:
        unlock()
`), []string{"python"}, extract.Options{ContextLines: 20, MaxCodeLines: 40})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	all := map[string]core.Finding{}
	for _, f := range got {
		all[f.CommentText] = f
	}
	cases := []struct{ comment, codeHas, codeNot string }{
		{"# Opens the menu when it is hidden.", "elif menu:", "close()"},
		{"# Falls back to the default layout.", "reset()", "open_menu()"},
		{"# A missing cache is not an error.", "except CacheMiss:", "load()"},
		{"# Releases the lock either way.", "unlock()", "warm()"},
	}
	for _, tc := range cases {
		f := all[tc.comment]
		if f.Kind != core.KindInline || !strings.Contains(f.CodeText, tc.codeHas) || strings.Contains(f.CodeText, tc.codeNot) {
			t.Errorf("%q: %s %q, want inline with %q and without %q", tc.comment, f.Kind, f.CodeText, tc.codeHas, tc.codeNot)
		}
	}
}
