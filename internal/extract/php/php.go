// Package php extracts comment/code pairs from PHP sources. It uses a pure-Go
// parser, so PHP needs no cgo and rides in every build.
package php

import (
	"bytes"
	"cmp"
	"reflect"
	"slices"
	"strings"

	"github.com/VKCOM/php-parser/pkg/ast"
	"github.com/VKCOM/php-parser/pkg/conf"
	phperrors "github.com/VKCOM/php-parser/pkg/errors"
	"github.com/VKCOM/php-parser/pkg/parser"
	"github.com/VKCOM/php-parser/pkg/token"
	"github.com/VKCOM/php-parser/pkg/version"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
)

type Extractor struct{}

func init() { extract.Register(Extractor{}) }

func (Extractor) Languages() []string  { return []string{"php"} }
func (Extractor) Extensions() []string { return []string{".php"} }

type span struct{ start, end, line int }

func byStart(a, b span) int { return cmp.Compare(a.start, b.start) }

type comment struct {
	span
	text    string
	isDoc   bool
	column  int
	ownLine bool // nothing but indentation before it on its line
}

func (c comment) isLine() bool { return !strings.HasPrefix(c.text, "/*") }

type item struct {
	span
	doc bool
}

// container holds the items the paragraph rule walks: statements in a block,
// members of a class, entries of an array, arguments, parameters.
type container struct {
	span
	items  []item
	parent int // index of the nearest container around it, or -1
}

type walker struct {
	src        []byte
	lineStarts []int
	comments   []comment
	stripped   *extract.Stripped
	containers []container
	decls      []span // what a docblock can document
	outer      []int  // index of the declaration around each one, or -1
	seen       map[*token.Token]bool
	options    extract.Options
}

func (Extractor) Extract(name string, src []byte, opts extract.Options) ([]core.Finding, error) {
	root, err := parser.Parse(src, conf.Config{
		Version:          &version.Version{Major: 8, Minor: 1},
		ErrorHandlerFunc: func(*phperrors.Error) {},
	})
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, nil
	}

	w := &walker{src: src, lineStarts: []int{0}, seen: map[*token.Token]bool{}, options: opts}
	for i, b := range src {
		if b == '\n' {
			w.lineStarts = append(w.lineStarts, i+1)
		}
	}
	w.walk(reflect.ValueOf(root))
	slices.SortFunc(w.comments, func(a, b comment) int { return byStart(a.span, b.span) })
	slices.SortFunc(w.decls, byStart)
	w.outer = nest(len(w.decls), func(i int) span { return w.decls[i] })
	// Of two containers starting together the outer one sorts first, so it
	// is the one nest finds open.
	slices.SortFunc(w.containers, func(a, b container) int {
		return cmp.Or(byStart(a.span, b.span), cmp.Compare(b.end, a.end))
	})
	for i, parent := range nest(len(w.containers), func(i int) span { return w.containers[i].span }) {
		w.containers[i].parent = parent
	}
	var cuts []core.Span
	for _, c := range w.comments {
		cuts = append(cuts, core.Span{Start: c.start, End: c.end})
	}
	w.stripped = extract.Strip(src, cuts)

	var findings []core.Finding
	for _, c := range w.groups() {
		findings = append(findings, w.pair(name, c)...)
	}
	return findings, nil
}

// nest finds the span around each of n spans sorted by start, as an index or
// -1.
func nest(n int, at func(int) span) []int {
	var open, outer []int
	for i := range n {
		for len(open) > 0 && at(open[len(open)-1]).end < at(i).end {
			open = open[:len(open)-1]
		}
		parent := -1
		if len(open) > 0 {
			parent = open[len(open)-1]
		}
		outer = append(outer, parent)
		open = append(open, i)
	}
	return outer
}

// walk reaches every node and token by reflection: the AST has no generic
// child accessor, and comments live in the tokens' free-floating lists.
func (w *walker) walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return
		}
		if tok, ok := v.Interface().(*token.Token); ok {
			w.collectTokens(tok)
			return
		}
		if node, ok := v.Interface().(ast.Vertex); ok {
			w.record(node)
		}
		w.walk(v.Elem())
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			w.walk(v.Index(i))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			if brackets, ok := itemLists[field.Name]; ok {
				w.addContainer(v, field.Name, brackets)
			}
			w.walk(v.Field(i))
		}
	}
}

var itemLists = map[string][2]string{
	"Stmts":  {"OpenCurlyBracketTkn", "CloseCurlyBracketTkn"},
	"Cases":  {"OpenCurlyBracketTkn", "CloseCurlyBracketTkn"},
	"Arms":   {"OpenCurlyBracketTkn", "CloseCurlyBracketTkn"},
	"Items":  {"OpenBracketTkn", "CloseBracketTkn"},
	"Args":   {"OpenParenthesisTkn", "CloseParenthesisTkn"},
	"Params": {"OpenParenthesisTkn", "CloseParenthesisTkn"},
}

func (w *walker) addContainer(node reflect.Value, list string, brackets [2]string) {
	c := container{span: w.bracketed(node, brackets)}
	if c.end <= c.start {
		return
	}
	items := node.FieldByName(list)
	for i := 0; i < items.Len(); i++ {
		vertex, ok := items.Index(i).Interface().(ast.Vertex)
		if !ok || reflect.ValueOf(vertex).IsNil() {
			continue
		}
		if s, ok := spanOf(vertex); ok {
			c.items = append(c.items, item{span: s, doc: documentable[reflect.TypeOf(vertex).Elem().Name()]})
		}
	}
	w.containers = append(w.containers, c)
}

// bracketed is the span of a node's item list: its brackets when it has them,
// otherwise the node, as for a `case` or the file itself.
func (w *walker) bracketed(node reflect.Value, brackets [2]string) span {
	if node.Type() == reflect.TypeFor[ast.Root]() {
		return span{start: 0, end: len(w.src), line: 1}
	}
	open, closing := tokenField(node, brackets[0]), tokenField(node, brackets[1])
	if open != nil && closing != nil && open.Position != nil && closing.Position != nil {
		return span{start: open.Position.StartPos, end: closing.Position.EndPos, line: open.Position.StartLine}
	}
	if vertex, ok := node.Addr().Interface().(ast.Vertex); ok {
		if s, ok := spanOf(vertex); ok {
			return s
		}
	}
	return span{}
}

// spanOf works around the parser ending a `case` that has no statements at -1:
// such a node ends where its last token does.
func spanOf(node ast.Vertex) (span, bool) {
	pos := node.GetPosition()
	if pos == nil {
		return span{}, false
	}
	s := span{start: pos.StartPos, end: pos.EndPos, line: pos.StartLine}
	if s.end < s.start {
		s.end = lastToken(reflect.ValueOf(node))
	}
	return s, s.end > s.start
}

func lastToken(v reflect.Value) int {
	end := -1
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return end
		}
		if tok, ok := v.Interface().(*token.Token); ok {
			if tok.Position != nil {
				end = tok.Position.EndPos
			}
			return end
		}
		return lastToken(v.Elem())
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			end = max(end, lastToken(v.Index(i)))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				end = max(end, lastToken(v.Field(i)))
			}
		}
	}
	return end
}

func tokenField(node reflect.Value, name string) *token.Token {
	field := node.FieldByName(name)
	if !field.IsValid() {
		return nil
	}
	tok, _ := field.Interface().(*token.Token)
	return tok
}

func (w *walker) collectTokens(tok *token.Token) {
	if tok == nil || w.seen[tok] {
		return
	}
	w.seen[tok] = true
	for _, free := range tok.FreeFloating {
		if free.ID != token.T_COMMENT && free.ID != token.T_DOC_COMMENT {
			continue
		}
		if free.Position == nil {
			continue
		}
		start := free.Position.StartPos
		text := strings.TrimRight(string(free.Value), " \t\r\n")
		w.comments = append(w.comments, comment{
			span:    span{start: start, end: start + len(text), line: free.Position.StartLine},
			text:    text,
			isDoc:   free.ID == token.T_DOC_COMMENT,
			column:  w.column(start),
			ownLine: len(bytes.TrimSpace(w.src[w.lineStarts[w.line(start)-1]:start])) == 0,
		})
	}
}

var documentable = map[string]bool{
	"StmtFunction": true, "StmtClassMethod": true, "StmtClass": true,
	"StmtInterface": true, "StmtTrait": true, "StmtPropertyList": true,
	"StmtClassConstList": true, "StmtConstList": true, "StmtEnum": true,
}

func (w *walker) record(node ast.Vertex) {
	pos := node.GetPosition()
	if pos == nil {
		return
	}
	name := reflect.TypeOf(node).Elem().Name()
	if documentable[name] {
		w.decls = append(w.decls, span{start: pos.StartPos, end: pos.EndPos, line: pos.StartLine})
	}
}

// groups merges consecutive line comments alone on their lines at one
// indentation, as the tree-sitter walker does, so a comment wrapped over
// several lines is judged whole.
func (w *walker) groups() []comment {
	var out []comment
	for _, c := range w.comments {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.isLine() && c.isLine() && last.ownLine && c.ownLine &&
				w.line(c.start) == w.line(last.end-1)+1 && c.column == last.column {
				last.end = c.end
				last.text = string(w.src[last.start:last.end])
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// pair applies the rules of §4.3.
func (w *walker) pair(file string, c comment) []core.Finding {
	kind, code, ok := w.code(c)
	if !ok {
		return nil
	}
	codeText, codeSpan := w.text(code)
	base := core.Finding{
		File:      file,
		Comment:   core.Span{Start: c.start, End: c.end},
		Code:      codeSpan,
		Kind:      kind,
		CodeText:  codeText,
		Context:   w.context(codeSpan),
		Line:      c.line,
		Column:    c.column,
		Protected: kind == core.KindDoc,
	}

	if !c.isDoc {
		base.CommentText = c.text
		return []core.Finding{base}
	}
	return extract.SplitDocblock(base, c.text)
}

func (w *walker) code(c comment) (core.Kind, span, bool) {
	box := w.innermost(c.span)
	// A docblock documents whatever comes next. Inside a method body that is a
	// statement, not the next declaration: `/** @var Foo $bar */` over a local
	// is the idiom static analysers rely on.
	if c.isDoc {
		if next, ok := box.after(c.end); ok && next.doc {
			return core.KindDoc, next.span, true
		}
		if run, ok := w.paragraph(box, c); ok {
			return core.KindInline, run, true
		}
	}
	if !c.ownLine {
		if it, ok := w.sameLine(box, c); ok {
			return core.KindTrailing, it, true
		}
	}
	// A comment inside an expression, such as between chained calls, belongs
	// to the statement around it rather than to the next one.
	if it, ok := box.around(c.span); ok {
		return inside(c), it, true
	}
	if run, ok := w.paragraph(box, c); ok {
		return core.KindInline, run, true
	}
	if it, ok := box.before(c.start); ok {
		return core.KindTrailing, it, true
	}
	// A comment alone in an empty body, such as a closure's, is graded against
	// what holds the body rather than skipped.
	for i := box.parent; i >= 0; i = w.containers[i].parent {
		if it, ok := w.containers[i].around(c.span); ok {
			return inside(c), it, true
		}
	}
	return "", span{}, false
}

func inside(c comment) core.Kind {
	if c.ownLine {
		return core.KindInline
	}
	return core.KindTrailing
}

// innermost climbs from the last container to start before s: any container
// holding s is among its ancestors. Of two sharing a span, the outer one wins.
func (w *walker) innermost(s span) container {
	i, _ := slices.BinarySearchFunc(w.containers, s.start+1, func(c container, start int) int { return cmp.Compare(c.start, start) })
	for i--; i >= 0; i = w.containers[i].parent {
		c := w.containers[i]
		if s.end > c.end {
			continue
		}
		for c.parent >= 0 && w.containers[c.parent].start == c.start && w.containers[c.parent].end == c.end {
			c = w.containers[c.parent]
		}
		return c
	}
	return container{parent: -1}
}

func (c container) from(offset int) int {
	i, _ := slices.BinarySearchFunc(c.items, offset, func(it item, start int) int { return cmp.Compare(it.start, start) })
	return i
}

func (c container) after(offset int) (item, bool) {
	i := c.from(offset)
	if i == len(c.items) {
		return item{}, false
	}
	return c.items[i], true
}

func (c container) around(s span) (span, bool) {
	i := c.from(s.start + 1)
	if i == 0 || c.items[i-1].end < s.end {
		return span{}, false
	}
	return c.items[i-1].span, true
}

func (c container) before(offset int) (span, bool) {
	i, _ := slices.BinarySearchFunc(c.items, offset+1, func(it item, end int) int { return cmp.Compare(it.end, end) })
	if i == 0 {
		return span{}, false
	}
	return c.items[i-1].span, true
}

func (w *walker) sameLine(box container, c comment) (span, bool) {
	it, ok := box.before(c.start)
	return it, ok && w.line(it.end-1) == c.line
}

// paragraph is the run of items a comment heads, ending at a blank line, the
// next comment, or the end of its container.
func (w *walker) paragraph(box container, c comment) (span, bool) {
	var run []span
	for _, it := range box.items[box.from(c.end):] {
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
	return span{start: run[0].start, end: run[len(run)-1].end, line: run[0].line}, true
}

func (w *walker) commentBetween(from, to int) bool {
	i, _ := slices.BinarySearchFunc(w.comments, from+1, func(c comment, start int) int { return cmp.Compare(c.start, start) })
	return i < len(w.comments) && w.comments[i].end < to
}

func (w *walker) text(s span) (string, core.Span) {
	start, end := clamp(s.start, len(w.src)), clamp(s.end, len(w.src))
	out := core.Span{Start: start, End: end}
	text := string(w.src[start:end])
	lines := strings.Split(text, "\n")
	if len(lines) <= w.options.MaxCodeLines {
		return text, out
	}
	kept := strings.Join(lines[:w.options.MaxCodeLines], "\n")
	return kept + "\n// ... truncated by scrutus", core.Span{Start: start, End: start + len(kept)}
}

func (w *walker) context(code core.Span) string {
	scope, ok := w.enclosingDecl(code)
	if !ok {
		return ""
	}
	return w.stripped.Context(core.Span{Start: scope.start, End: scope.end}, code, w.options.ContextLines)
}

// enclosingDecl climbs from the last declaration to start before the code:
// declarations nest, so any that holds the code is among its outers.
func (w *walker) enclosingDecl(code core.Span) (span, bool) {
	for i := seek(w.decls, code.Start+1) - 1; i >= 0; i = w.outer[i] {
		if code.End <= w.decls[i].end {
			return w.decls[i], true
		}
	}
	return span{}, false
}

// seek is the index of the first span starting at or after offset.
func seek(spans []span, offset int) int {
	i, _ := slices.BinarySearchFunc(spans, offset, func(s span, offset int) int { return cmp.Compare(s.start, offset) })
	return i
}

func (w *walker) line(offset int) int {
	n, _ := slices.BinarySearch(w.lineStarts, clamp(offset, len(w.src))+1)
	return n
}

func (w *walker) column(offset int) int {
	offset = clamp(offset, len(w.src))
	return offset - w.lineStarts[w.line(offset)-1] + 1
}

func clamp(v, limit int) int { return max(0, min(v, limit)) }
