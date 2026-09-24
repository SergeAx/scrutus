// Package php extracts comment/code pairs from PHP sources. It uses a pure-Go
// parser, so PHP needs no cgo and rides in every build.
package php

import (
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
	text   string
	isDoc  bool
	column int
}

type walker struct {
	src        []byte
	lineStarts []int
	comments   []comment
	stripped   *extract.Stripped
	statements []span // every statement in a body, for the paragraph rule
	endings    []span // statements by the line they end on, latest start first
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
	w.walk(reflect.ValueOf(root), false)
	slices.SortFunc(w.comments, func(a, b comment) int { return byStart(a.span, b.span) })
	slices.SortFunc(w.statements, byStart)
	slices.SortFunc(w.decls, byStart)
	w.nest()
	w.endings = slices.Clone(w.statements)
	slices.SortFunc(w.endings, func(a, b span) int {
		return cmp.Or(cmp.Compare(w.line(a.end-1), w.line(b.end-1)), cmp.Compare(b.start, a.start))
	})
	var cuts []core.Span
	for _, c := range w.comments {
		cuts = append(cuts, core.Span{Start: c.start, End: c.end})
	}
	w.stripped = extract.Strip(src, cuts)

	var findings []core.Finding
	for _, c := range w.comments {
		findings = append(findings, w.pair(name, c)...)
	}
	return findings, nil
}

func (w *walker) nest() {
	var open []int
	for i, decl := range w.decls {
		for len(open) > 0 && w.decls[open[len(open)-1]].end < decl.end {
			open = open[:len(open)-1]
		}
		outer := -1
		if len(open) > 0 {
			outer = open[len(open)-1]
		}
		w.outer = append(w.outer, outer)
		open = append(open, i)
	}
}

// walk reaches every node and token by reflection: the AST has no generic
// child accessor, and comments live in the tokens' free-floating lists.
func (w *walker) walk(v reflect.Value, inBody bool) {
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
		w.walk(v.Elem(), inBody)
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			w.walk(v.Index(i), inBody)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			if field.Name == "Stmts" {
				w.recordStatements(v.Field(i))
			}
			w.walk(v.Field(i), inBody)
		}
	}
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
		text := strings.TrimRight(string(free.Value), " \t\r\n")
		w.comments = append(w.comments, comment{
			start:  free.Position.StartPos,
			end:    free.Position.StartPos + len(text),
			line:   free.Position.StartLine,
			text:   text,
			isDoc:  free.ID == token.T_DOC_COMMENT,
			column: w.column(free.Position.StartPos),
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

func (w *walker) recordStatements(stmts reflect.Value) {
	if stmts.Kind() != reflect.Slice {
		return
	}
	for i := 0; i < stmts.Len(); i++ {
		node, ok := stmts.Index(i).Interface().(ast.Vertex)
		if !ok || node == nil {
			continue
		}
		if pos := node.GetPosition(); pos != nil {
			w.statements = append(w.statements, span{start: pos.StartPos, end: pos.EndPos, line: pos.StartLine})
		}
	}
}

// pair applies the rules of §4.3.
func (w *walker) pair(file string, c comment) []core.Finding {
	kind, code, protected := w.code(c)
	if code.end <= code.start {
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
		Protected: protected,
	}

	if !c.isDoc {
		base.CommentText = c.text
		return []core.Finding{base}
	}
	return extract.SplitDocblock(base, c.text)
}

func (w *walker) code(c comment) (core.Kind, span, bool) {
	// A docblock documents whatever comes next. Inside a method body that is a
	// statement, not the next declaration: `/** @var Foo $bar */` over a local
	// is the idiom static analysers rely on.
	if c.isDoc {
		decl, hasDecl := w.nextDecl(c.end)
		paragraph, hasParagraph := w.paragraph(c)
		if hasDecl && (!hasParagraph || decl.start <= paragraph.start) {
			return core.KindDoc, decl, true
		}
		if hasParagraph {
			return core.KindInline, paragraph, false
		}
	}
	if stmt, ok := w.sameLineStatement(c); ok {
		return core.KindTrailing, stmt, false
	}
	if paragraph, ok := w.paragraph(c); ok {
		return core.KindInline, paragraph, false
	}
	if decl, ok := w.nextDecl(c.end); ok {
		return core.KindInline, decl, false
	}
	if stmt, ok := w.precedingStatement(c); ok {
		return core.KindTrailing, stmt, false
	}
	return core.KindInline, span{}, false
}

// seek is the index of the first span starting at or after offset.
func seek(spans []span, offset int) int {
	i, _ := slices.BinarySearchFunc(spans, offset, func(s span, offset int) int { return cmp.Compare(s.start, offset) })
	return i
}

func (w *walker) nextDecl(after int) (span, bool) {
	i := seek(w.decls, after)
	if i == len(w.decls) {
		return span{}, false
	}
	return w.decls[i], true
}

func (w *walker) sameLineStatement(c comment) (span, bool) {
	i, _ := slices.BinarySearchFunc(w.endings, c.line, func(s span, line int) int { return cmp.Compare(w.line(s.end-1), line) })
	for _, stmt := range w.endings[i:] {
		if w.line(stmt.end-1) != c.line {
			break
		}
		if stmt.end <= c.start {
			return stmt, true
		}
	}
	return span{}, false
}

// paragraph is the run of statements a comment heads, ending at a blank line,
// the next comment, or the end of the enclosing body.
func (w *walker) paragraph(c comment) (span, bool) {
	var run []span
	for _, stmt := range w.statements[seek(w.statements, c.end):] {
		if len(run) == 0 {
			if w.enclosingStatement(stmt, c) {
				continue
			}
			run = append(run, stmt)
			continue
		}
		previous := run[len(run)-1]
		if stmt.start < previous.end {
			continue
		}
		if w.line(stmt.start)-w.line(previous.end-1) > 1 || w.commentBetween(previous.end, stmt.start) {
			break
		}
		run = append(run, stmt)
	}
	if len(run) == 0 {
		return span{}, false
	}
	return span{start: run[0].start, end: run[len(run)-1].end, line: run[0].line}, true
}

// enclosingStatement skips the block a comment sits inside: its own body's
// statements are the paragraph, not the block itself.
func (w *walker) enclosingStatement(stmt span, c comment) bool {
	return stmt.start <= c.start && stmt.end >= c.end
}

func (w *walker) precedingStatement(c comment) (span, bool) {
	for i := seek(w.statements, c.start+1) - 1; i >= 0; i-- {
		if stmt := w.statements[i]; stmt.end <= c.start {
			return stmt, true
		}
	}
	return span{}, false
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

func (w *walker) line(offset int) int {
	n, _ := slices.BinarySearch(w.lineStarts, clamp(offset, len(w.src))+1)
	return n
}

func (w *walker) column(offset int) int {
	offset = clamp(offset, len(w.src))
	return offset - w.lineStarts[w.line(offset)-1] + 1
}

func clamp(v, limit int) int { return max(0, min(v, limit)) }
