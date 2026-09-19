// Package php extracts comment/code pairs from PHP sources. It uses a pure-Go
// parser, so PHP needs no cgo and rides in every build.
package php

import (
	"reflect"
	"regexp"
	"sort"
	"strconv"
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

type comment struct {
	span
	text   string
	isDoc  bool
	column int
}

type walker struct {
	src        []byte
	comments   []comment
	statements []span // every statement in a body, for the paragraph rule
	decls      []span // what a docblock can document
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

	w := &walker{src: src, seen: map[*token.Token]bool{}, options: opts}
	w.walk(reflect.ValueOf(root), false)
	sort.Slice(w.comments, func(i, j int) bool { return w.comments[i].start < w.comments[j].start })
	sort.Slice(w.statements, func(i, j int) bool { return w.statements[i].start < w.statements[j].start })
	sort.Slice(w.decls, func(i, j int) bool { return w.decls[i].start < w.decls[j].start })

	var findings []core.Finding
	for _, c := range w.comments {
		findings = append(findings, w.pair(name, c)...)
	}
	return findings, nil
}

// walk reaches every node and token by reflection: the AST has no generic
// child accessor, and comments live in the tokens' free-floating lists.
func (w *walker) walk(v reflect.Value, inBody bool) {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
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
		w.comments = append(w.comments, comment{
			span:   span{start: free.Position.StartPos, end: free.Position.EndPos, line: free.Position.StartLine},
			text:   strings.TrimRight(string(free.Value), " \t\r\n"),
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

// pair applies the rules of §4.3, splitting a docblock into its free text and
// one finding per annotation tag.
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
		Line:      c.line,
		Column:    c.column,
		Protected: protected,
	}
	base.Context = w.context(code, codeSpan)

	if !c.isDoc {
		base.CommentText = c.text
		return []core.Finding{base}
	}

	free, proseEnd, annotations := splitDocblock(c.text)
	blockKey := file + ":" + strconv.Itoa(c.start)
	var out []core.Finding
	if strings.TrimSpace(strings.Trim(free, "/* \t\n")) != "" {
		summary := base
		summary.CommentText = free
		summary.Comment = core.Span{Start: c.start, End: c.start + proseEnd}
		summary.Block = blockKey
		out = append(out, summary)
	}
	for i, annotation := range annotations {
		tag := base
		tag.Kind = core.KindAnnotation
		tag.CommentText = annotation.text
		tag.Comment = core.Span{Start: c.start + annotation.offset, End: c.start + annotation.offset + len(annotation.text)}
		tag.Line = c.line + annotation.lineOffset
		tag.Block = blockKey
		tag.Slot = "a" + strconv.Itoa(i+1)
		tag.Protected = false
		out = append(out, tag)
	}
	return out
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

func (w *walker) nextDecl(after int) (span, bool) {
	for _, decl := range w.decls {
		if decl.start >= after {
			return decl, true
		}
	}
	return span{}, false
}

func (w *walker) sameLineStatement(c comment) (span, bool) {
	var best span
	found := false
	for _, stmt := range w.statements {
		if stmt.end <= c.start && w.line(stmt.end-1) == c.line {
			if !found || stmt.start > best.start {
				best, found = stmt, true
			}
		}
	}
	return best, found
}

// paragraph is the run of statements a comment heads, ending at a blank line,
// the next comment, or the end of the enclosing body.
func (w *walker) paragraph(c comment) (span, bool) {
	var run []span
	for _, stmt := range w.statements {
		if stmt.start < c.end {
			continue
		}
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
	var best span
	found := false
	for _, stmt := range w.statements {
		if stmt.end <= c.start && (!found || stmt.start > best.start) {
			best, found = stmt, true
		}
	}
	return best, found
}

func (w *walker) commentBetween(from, to int) bool {
	for _, c := range w.comments {
		if c.start > from && c.end < to {
			return true
		}
	}
	return false
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

// context is the enclosing declaration with comments stripped and the paired
// code delimited.
func (w *walker) context(code span, codeSpan core.Span) string {
	scope, ok := w.enclosingDecl(codeSpan)
	if !ok {
		return ""
	}
	var b strings.Builder
	b.Write(w.stripComments(scope.start, codeSpan.Start))
	b.WriteString(">>> CODE\n")
	b.Write(w.src[codeSpan.Start:codeSpan.End])
	b.WriteString("\n<<< CODE\n")
	b.Write(w.stripComments(codeSpan.End, clamp(scope.end, len(w.src))))
	return capContext(b.String(), w.options.ContextLines)
}

func (w *walker) enclosingDecl(code core.Span) (span, bool) {
	var found span
	ok := false
	for _, decl := range w.decls {
		if decl.start <= code.Start && code.End <= decl.end {
			if !ok || (decl.end-decl.start) < (found.end-found.start) {
				found, ok = decl, true
			}
		}
	}
	return found, ok
}

func (w *walker) stripComments(start, end int) []byte {
	start, end = clamp(start, len(w.src)), clamp(end, len(w.src))
	out := make([]byte, 0, max(0, end-start))
	cursor := start
	for _, c := range w.comments {
		if c.end <= cursor || c.start >= end {
			continue
		}
		if c.start > cursor {
			out = append(out, w.src[cursor:min(c.start, end)]...)
		}
		cursor = max(cursor, min(c.end, end))
	}
	if cursor < end {
		out = append(out, w.src[cursor:end]...)
	}
	return out
}

func capContext(text string, limit int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if limit <= 0 || len(lines) <= limit {
		return strings.Join(lines, "\n")
	}
	mark := 0
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), ">>> CODE") {
			mark = i
			break
		}
	}
	start := max(0, mark-limit/2)
	return strings.Join(lines[start:min(len(lines), start+limit)], "\n")
}

func (w *walker) line(offset int) int {
	offset = clamp(offset, len(w.src))
	return strings.Count(string(w.src[:offset]), "\n") + 1
}

func (w *walker) column(offset int) int {
	offset = clamp(offset, len(w.src))
	lineStart := strings.LastIndexByte(string(w.src[:offset]), '\n') + 1
	return offset - lineStart + 1
}

type annotation struct {
	text       string
	offset     int
	lineOffset int
}

var annotationLine = regexp.MustCompile(`^\s*(?:/\*\*?)?\s*\*?\s*(@[A-Za-z][\w-]*)`)

// splitDocblock separates a docblock's prose from its tags, so a redundant
// @var is graded on its own and a useful summary is not dragged down with it.
func splitDocblock(text string) (prose string, proseEnd int, tags []annotation) {
	lines := strings.Split(text, "\n")
	var free []string
	offset, proseEnd := 0, len(text)
	for i, line := range lines {
		if annotationLine.MatchString(line) {
			trimmed := strings.TrimLeft(line, " \t*/")
			if len(tags) == 0 {
				proseEnd = offset
			}
			tags = append(tags, annotation{
				text:       strings.TrimRight(strings.TrimSuffix(strings.TrimRight(trimmed, " \t\r"), "*/"), " \t"),
				offset:     offset + (len(line) - len(trimmed)),
				lineOffset: i,
			})
		} else {
			free = append(free, line)
		}
		offset += len(line) + 1
	}
	return strings.Join(free, "\n"), proseEnd, tags
}

func clamp(v, limit int) int { return max(0, min(v, limit)) }
