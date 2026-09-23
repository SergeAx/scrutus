//go:build cgo

package treesitter

import (
	"bytes"
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
)

// grammar is what one language contributes to the pairing rules of §4.3; the
// rules themselves are shared.
type grammar struct {
	name       string
	extensions []string
	language   func(ext string) *sitter.Language
	truncation string

	// containers hold the items the paragraph rule walks: statements in a
	// block, members of a class, entries of a literal.
	containers map[string]bool
	// itemField limits a container's items to one field, for nodes such as
	// `case x:` whose value is not an item.
	itemField map[string]string
	// headed containers start at their first item rather than at the header
	// that opens them, so a comment right under the header would fall outside.
	headed       map[string]bool
	scopes       map[string]bool
	documentable func(item *sitter.Node, inFunction bool) bool
	directive    func(text string) bool
	docstring    func(n *sitter.Node, src []byte) (docstring, bool)
}

// docstring is a string statement that Python treats as the doc comment of
// the declaration it opens.
type docstring struct {
	stmt, text, decl span
	// sole marks a docstring that is its body's only statement, so deleting it
	// would leave the body empty.
	sole bool
}

type Extractor struct{ g *grammar }

func (e Extractor) Languages() []string  { return []string{e.g.name} }
func (e Extractor) Extensions() []string { return e.g.extensions }

func (e Extractor) Extract(name string, src []byte, opts extract.Options) ([]core.Finding, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(e.g.language(strings.ToLower(filepath.Ext(name)))); err != nil {
		return nil, err
	}

	tree := parser.Parse(src, nil)
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil, syntaxError(root)
	}

	w := newWalker(e.g, src, opts)
	w.walk(root, false, true)
	return w.findings(name), nil
}

// syntaxError names the first line tree-sitter could not parse. The parser
// recovers from any input, so an error node is the only failure signal, and
// fix relies on it to refuse an edit that broke the file.
func syntaxError(n *sitter.Node) error {
	for !n.IsError() && !n.IsMissing() {
		var next *sitter.Node
		for _, c := range children(n) {
			if c.node.HasError() {
				next = c.node
				break
			}
		}
		if next == nil {
			break
		}
		n = next
	}
	return fmt.Errorf("syntax error at line %d", n.StartPosition().Row+1)
}

type span struct{ start, end int }

func spanOf(n *sitter.Node) span { return span{int(n.StartByte()), int(n.EndByte())} }

type item struct {
	span
	doc bool
}

type container struct {
	span
	items []item
}

type comment struct {
	span
	ownLine bool // nothing but indentation before it on its line
	line    bool // a line comment, which merges with the ones right below it
}

type child struct {
	node  *sitter.Node
	field string
}

// children indexes rather than walking a TreeCursor: creating and deleting one
// per node costs two trips through the binding's Go allocator hooks.
func children(n *sitter.Node) []child {
	count := n.ChildCount()
	out := make([]child, 0, count)
	for i := range count {
		out = append(out, child{n.Child(i), n.FieldNameForChild(uint32(i))})
	}
	return out
}

type walker struct {
	g          *grammar
	src        []byte
	lineStarts []int
	comments   []comment
	containers []container
	docstrings []docstring
	scopes     []span
	stripped   *extract.Stripped
	opts       extract.Options
}

func newWalker(g *grammar, src []byte, opts extract.Options) *walker {
	w := &walker{g: g, src: src, lineStarts: []int{0}, opts: opts}
	for i, b := range src {
		if b == '\n' {
			w.lineStarts = append(w.lineStarts, i+1)
		}
	}
	return w
}

func (w *walker) walk(n *sitter.Node, inFunction, root bool) {
	kids := children(n)
	if w.g.containers[n.Kind()] {
		c := w.container(n, kids, inFunction)
		w.containers = append(w.containers, c)
		if root {
			for _, it := range c.items {
				w.scopes = append(w.scopes, it.span)
			}
		}
	}
	if w.g.scopes[n.Kind()] {
		w.scopes = append(w.scopes, spanOf(n))
		inFunction = true
	}
	if w.g.docstring != nil {
		if d, ok := w.g.docstring(n, w.src); ok {
			w.docstrings = append(w.docstrings, d)
		}
	}
	for _, k := range kids {
		switch {
		case !k.node.IsNamed():
		case k.node.Kind() == "comment":
			w.addComment(k.node)
		default:
			w.walk(k.node, inFunction, false)
		}
	}
}

func (w *walker) container(n *sitter.Node, kids []child, inFunction bool) container {
	c := container{span: spanOf(n)}
	if w.g.headed[n.Kind()] {
		for p := n.PrevSibling(); p != nil; p = p.PrevSibling() {
			if p.Kind() != "comment" {
				c.start = int(p.EndByte())
				break
			}
		}
	}
	field := w.g.itemField[n.Kind()]
	decorated := -1
	for _, k := range kids {
		typ := k.node.Kind()
		if !k.node.IsNamed() || typ == "comment" || typ == "hash_bang_line" || (field != "" && k.field != field) {
			continue
		}
		// A class member's decorators are its siblings, yet a comment above
		// them documents the member.
		if typ == "decorator" {
			if decorated < 0 {
				decorated = int(k.node.StartByte())
			}
			continue
		}
		it := item{span: spanOf(k.node), doc: w.g.documentable != nil && w.g.documentable(k.node, inFunction)}
		if decorated >= 0 {
			it.start, decorated = decorated, -1
		}
		c.items = append(c.items, it)
	}
	return c
}

func (w *walker) addComment(n *sitter.Node) {
	s := spanOf(n)
	for s.end > s.start && (w.src[s.end-1] == '\r' || w.src[s.end-1] == ' ' || w.src[s.end-1] == '\t') {
		s.end--
	}
	text := string(w.src[s.start:s.end])
	if w.g.directive != nil && w.g.directive(text) {
		return
	}
	w.comments = append(w.comments, comment{
		span:    s,
		ownLine: len(bytes.TrimSpace(w.src[w.lineStart(s.start):s.start])) == 0,
		line:    !strings.HasPrefix(text, "/*"),
	})
}

func (w *walker) findings(file string) []core.Finding {
	slices.SortFunc(w.comments, func(a, b comment) int { return cmp.Compare(a.start, b.start) })
	var cuts []core.Span
	for _, c := range w.comments {
		cuts = append(cuts, core.Span{Start: c.start, End: c.end})
	}
	for _, d := range w.docstrings {
		cuts = append(cuts, core.Span{Start: d.stmt.start, End: d.stmt.end})
	}
	slices.SortFunc(cuts, func(a, b core.Span) int { return cmp.Compare(a.Start, b.Start) })
	w.stripped = extract.Strip(w.src, cuts)

	var out []core.Finding
	for _, c := range w.groups() {
		out = append(out, w.pair(file, c)...)
	}
	for _, d := range w.docstrings {
		out = append(out, w.docstringFinding(file, d))
	}
	return out
}

// docstringFinding carries no Context: its code is the whole declaration
// already, and the context renderer would print the docstring back inside the
// marked code.
func (w *walker) docstringFinding(file string, d docstring) core.Finding {
	codeText, _ := w.capped(w.without(d.decl, d.stmt))
	return core.Finding{
		File:        file,
		Comment:     core.Span{Start: d.stmt.start, End: d.stmt.end},
		Code:        core.Span{Start: d.decl.start, End: d.decl.end},
		Kind:        core.KindDoc,
		CommentText: string(w.src[d.text.start:d.text.end]),
		CodeText:    codeText,
		Line:        w.line(d.stmt.start),
		Column:      w.column(d.stmt.start),
		Protected:   true,
		Required:    d.sole,
	}
}

// without is the text of decl with cut removed, taking cut's whole lines when
// nothing else shares them.
func (w *walker) without(decl, cut span) string {
	from, to := cut.start, cut.end
	if lineStart := w.lineStart(from); len(bytes.TrimSpace(w.src[lineStart:from])) == 0 {
		from = lineStart
	}
	if newline := bytes.IndexByte(w.src[to:], '\n'); newline >= 0 && len(bytes.TrimSpace(w.src[to:to+newline])) == 0 {
		to += newline + 1
	}
	from, to = max(from, decl.start), min(to, decl.end)
	return string(w.src[decl.start:from]) + string(w.src[to:decl.end])
}

// groups merges consecutive own-line line comments into one, as go/ast does,
// but only at one indentation: in Python a dedent ends the block they
// describe.
func (w *walker) groups() []comment {
	var out []comment
	for _, c := range w.comments {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.line && c.line && last.ownLine && c.ownLine &&
				w.line(c.start) == w.line(last.end-1)+1 && w.column(c.start) == w.column(last.start) {
				last.end = c.end
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func (w *walker) pair(file string, c comment) []core.Finding {
	kind, code, ok := w.code(c)
	if !ok {
		return nil
	}
	codeText, codeSpan := w.text(code)
	f := core.Finding{
		File:        file,
		Comment:     core.Span{Start: c.start, End: c.end},
		Code:        codeSpan,
		Kind:        kind,
		CommentText: string(w.src[c.start:c.end]),
		CodeText:    codeText,
		Context:     w.context(c.start, codeSpan),
		Line:        w.line(c.start),
		Column:      w.column(c.start),
	}
	if strings.HasPrefix(f.CommentText, "/**") {
		return extract.SplitDocblock(f, f.CommentText)
	}
	return []core.Finding{f}
}

func (w *walker) code(c comment) (core.Kind, span, bool) {
	box := w.innermost(c.span)
	if c.ownLine {
		if next, ok := box.after(c.end); ok && next.doc && w.adjacent(c.end, next.start) {
			return core.KindDoc, next.span, true
		}
	} else if it, ok := w.sameLine(box, c); ok {
		return core.KindTrailing, it, true
	}
	// A comment inside an expression, such as between chained calls, belongs
	// to the statement around it rather than to the next one.
	if it, ok := box.around(c.span); ok {
		if c.ownLine {
			return core.KindInline, it, true
		}
		return core.KindTrailing, it, true
	}
	if run, ok := w.paragraph(box, c); ok {
		return core.KindInline, run, true
	}
	if it, ok := box.before(c.start); ok {
		return core.KindTrailing, it, true
	}
	return "", span{}, false
}

func (w *walker) innermost(s span) container {
	var found container
	ok := false
	for _, c := range w.containers {
		if c.start <= s.start && s.end <= c.end && (!ok || c.end-c.start < found.end-found.start) {
			found, ok = c, true
		}
	}
	if !ok && len(w.containers) > 0 {
		return w.containers[0]
	}
	return found
}

func (c container) after(offset int) (item, bool) {
	for _, it := range c.items {
		if it.start >= offset {
			return it, true
		}
	}
	return item{}, false
}

func (c container) around(s span) (span, bool) {
	for _, it := range c.items {
		if it.start <= s.start && s.end <= it.end {
			return it.span, true
		}
	}
	return span{}, false
}

func (c container) before(offset int) (span, bool) {
	var best span
	found := false
	for _, it := range c.items {
		if it.end <= offset {
			best, found = it.span, true
		}
	}
	return best, found
}

func (w *walker) sameLine(box container, c comment) (span, bool) {
	var best span
	found := false
	for _, it := range box.items {
		if it.end <= c.start && w.line(it.end-1) == w.line(c.start) {
			best, found = it.span, true
		}
	}
	return best, found
}

// paragraph is the run of items a comment heads, ending at a blank line, the
// next comment, or the end of the container.
func (w *walker) paragraph(box container, c comment) (span, bool) {
	var run []span
	for _, it := range box.items {
		if it.start < c.end {
			continue
		}
		if len(run) > 0 {
			previous := run[len(run)-1]
			if w.line(it.start)-w.line(previous.end-1) > 1 || w.commentBetween(previous.end, it.start) {
				break
			}
		}
		run = append(run, it.span)
	}
	if len(run) == 0 {
		return span{}, false
	}
	return span{run[0].start, run[len(run)-1].end}, true
}

// adjacent holds when only indentation and a single line break separate a
// comment from what follows, which is what makes it a doc comment.
func (w *walker) adjacent(from, to int) bool {
	gap := w.src[from:to]
	return len(bytes.TrimSpace(gap)) == 0 && bytes.Count(gap, []byte("\n")) <= 1
}

func (w *walker) commentBetween(from, to int) bool {
	for _, c := range w.comments {
		if c.start >= from && c.end <= to {
			return true
		}
	}
	return false
}

func (w *walker) context(commentStart int, code core.Span) string {
	var scope span
	ok := false
	for _, s := range w.scopes {
		if s.start <= commentStart && s.start <= code.Start && code.End <= s.end && (!ok || s.end-s.start < scope.end-scope.start) {
			scope, ok = s, true
		}
	}
	if !ok {
		return ""
	}
	return w.stripped.Context(core.Span{Start: scope.start, End: scope.end}, code, w.opts.ContextLines)
}

func (w *walker) text(s span) (string, core.Span) {
	text, kept := w.capped(string(w.src[s.start:s.end]))
	return text, core.Span{Start: s.start, End: s.start + kept}
}

// capped keeps the first MaxCodeLines lines of text and says how many of its
// bytes survived.
func (w *walker) capped(text string) (string, int) {
	lines := strings.Split(text, "\n")
	if len(lines) <= w.opts.MaxCodeLines {
		return text, len(text)
	}
	kept := strings.Join(lines[:w.opts.MaxCodeLines], "\n")
	return kept + "\n" + w.g.truncation, len(kept)
}

func (w *walker) line(offset int) int {
	n, _ := slices.BinarySearch(w.lineStarts, offset+1)
	return n
}

func (w *walker) lineStart(offset int) int { return w.lineStarts[w.line(offset)-1] }

func (w *walker) column(offset int) int { return offset - w.lineStart(offset) + 1 }

func set(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}
