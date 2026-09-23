// Package golang extracts comment/code pairs from Go sources with go/ast, so
// spans are exact byte offsets.
package golang

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"

	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/extract"
)

const truncationMarker = "\n\t// ... truncated by scrutus"

type Extractor struct{}

func init() { extract.Register(Extractor{}) }

func (Extractor) Languages() []string  { return []string{"go"} }
func (Extractor) Extensions() []string { return []string{".go"} }

type block struct {
	start, end token.Pos
	stmts      []ast.Stmt
}

type owner struct {
	start, end token.Pos
	exported   bool
}

type fileWalk struct {
	fset         *token.FileSet
	file         *token.File
	src          []byte
	owners       map[*ast.CommentGroup]owner
	blocks       []block
	scopes       []owner // enclosing declarations, for Context
	groups       []*ast.CommentGroup
	commentSpans []core.Span
	options      extract.Options
}

func (Extractor) Extract(name string, src []byte, opts extract.Options) ([]core.Finding, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	w := &fileWalk{
		fset:    fset,
		file:    fset.File(parsed.Pos()),
		src:     src,
		owners:  map[*ast.CommentGroup]owner{},
		groups:  parsed.Comments,
		options: opts,
	}
	w.collect(parsed)
	for _, group := range parsed.Comments {
		w.commentSpans = append(w.commentSpans, core.Span{Start: w.offset(group.Pos()), End: w.offset(group.End())})
	}

	var findings []core.Finding
	for _, group := range parsed.Comments {
		if f, ok := w.pair(name, group); ok {
			findings = append(findings, f)
		}
	}
	return findings, nil
}

func (w *fileWalk) collect(parsed *ast.File) {
	if parsed.Doc != nil {
		w.owners[parsed.Doc] = owner{start: parsed.Name.Pos(), end: parsed.Name.End(), exported: false}
	}
	for _, decl := range parsed.Decls {
		w.scopes = append(w.scopes, owner{start: decl.Pos(), end: decl.End()})
	}

	ast.Inspect(parsed, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			w.own(node.Doc, node.Pos(), node.End(), exportedName(node.Name))
		case *ast.GenDecl:
			w.own(node.Doc, node.Pos(), node.End(), genDeclExported(node))
		case *ast.TypeSpec:
			w.own(node.Doc, node.Pos(), node.End(), exportedName(node.Name))
			w.own(node.Comment, node.Pos(), node.End(), exportedName(node.Name))
		case *ast.ValueSpec:
			exported := len(node.Names) > 0 && exportedName(node.Names[0])
			w.own(node.Doc, node.Pos(), node.End(), exported)
			w.own(node.Comment, node.Pos(), node.End(), exported)
		case *ast.Field:
			exported := len(node.Names) > 0 && exportedName(node.Names[0])
			w.own(node.Doc, node.Pos(), node.End(), exported)
			w.own(node.Comment, node.Pos(), node.End(), exported)
		case *ast.BlockStmt:
			w.blocks = append(w.blocks, block{node.Lbrace, node.Rbrace, node.List})
		case *ast.CaseClause:
			w.blocks = append(w.blocks, block{node.Colon, node.End(), node.Body})
		case *ast.CommClause:
			w.blocks = append(w.blocks, block{node.Colon, node.End(), node.Body})
		}
		return true
	})
}

func (w *fileWalk) own(group *ast.CommentGroup, start, end token.Pos, exported bool) {
	if group == nil {
		return
	}
	if existing, ok := w.owners[group]; ok && existing.end-existing.start <= end-start {
		return // keep the tightest declaration
	}
	w.owners[group] = owner{start: start, end: end, exported: exported}
}

func exportedName(ident *ast.Ident) bool { return ident != nil && ast.IsExported(ident.Name) }

func genDeclExported(decl *ast.GenDecl) bool {
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if exportedName(s.Name) {
				return true
			}
		case *ast.ValueSpec:
			if slices.ContainsFunc(s.Names, exportedName) {
				return true
			}
		}
	}
	return false
}

func (w *fileWalk) pair(file string, group *ast.CommentGroup) (core.Finding, bool) {
	kind := core.KindInline
	var codeStart, codeEnd token.Pos
	protected := false

	if o, ok := w.owners[group]; ok && o.start > group.End() {
		kind, codeStart, codeEnd, protected = core.KindDoc, o.start, o.end, o.exported
	} else if start, end, ok := w.sameLineCode(group); ok {
		kind, codeStart, codeEnd = core.KindTrailing, start, end
	} else if start, end, ok := w.paragraph(group); ok {
		codeStart, codeEnd = start, end
	} else if start, end, ok := w.precedingCode(group); ok {
		kind, codeStart, codeEnd = core.KindTrailing, start, end
	} else {
		return core.Finding{}, false
	}

	codeText, codeSpan := w.text(codeStart, codeEnd, w.options.MaxCodeLines)
	position := w.fset.Position(group.Pos())
	return core.Finding{
		File:        file,
		Comment:     core.Span{Start: w.offset(group.Pos()), End: w.offset(group.End())},
		Code:        codeSpan,
		Kind:        kind,
		CommentText: string(w.src[w.offset(group.Pos()):w.offset(group.End())]),
		CodeText:    codeText,
		Context:     w.context(group, codeSpan),
		Line:        position.Line,
		Column:      position.Column,
		Protected:   protected,
	}, true
}

// sameLineCode finds a statement ending on the comment's line before it, which
// makes the comment trailing.
func (w *fileWalk) sameLineCode(group *ast.CommentGroup) (token.Pos, token.Pos, bool) {
	commentLine := w.line(group.Pos())
	var best ast.Stmt
	for _, b := range w.blocks {
		for _, stmt := range b.stmts {
			if stmt.End() <= group.Pos() && w.line(stmt.End()) == commentLine {
				if best == nil || stmt.Pos() > best.Pos() {
					best = stmt
				}
			}
		}
	}
	if best == nil {
		return 0, 0, false
	}
	return best.Pos(), best.End(), true
}

// paragraph implements §4.3: the run of statements a comment heads, ending at
// a blank line, the next comment, or the end of the enclosing block.
func (w *fileWalk) paragraph(group *ast.CommentGroup) (token.Pos, token.Pos, bool) {
	b, ok := w.innermost(group.Pos())
	if !ok {
		return w.fileLevelParagraph(group)
	}

	var run []ast.Stmt
	for _, stmt := range b.stmts {
		if stmt.Pos() < group.End() {
			continue
		}
		if len(run) == 0 {
			run = append(run, stmt)
			continue
		}
		previous := run[len(run)-1]
		if w.line(stmt.Pos())-w.line(previous.End()) > 1 || w.commentBetween(previous.End(), stmt.Pos()) {
			break
		}
		run = append(run, stmt)
	}
	if len(run) == 0 {
		return 0, 0, false
	}
	return run[0].Pos(), run[len(run)-1].End(), true
}

func (w *fileWalk) fileLevelParagraph(group *ast.CommentGroup) (token.Pos, token.Pos, bool) {
	for _, scope := range w.scopes {
		if scope.start > group.End() {
			return scope.start, scope.end, true
		}
	}
	return 0, 0, false
}

func (w *fileWalk) precedingCode(group *ast.CommentGroup) (token.Pos, token.Pos, bool) {
	b, ok := w.innermost(group.Pos())
	if !ok {
		return 0, 0, false
	}
	var best ast.Stmt
	for _, stmt := range b.stmts {
		if stmt.End() <= group.Pos() && (best == nil || stmt.Pos() > best.Pos()) {
			best = stmt
		}
	}
	if best == nil {
		return 0, 0, false
	}
	return best.Pos(), best.End(), true
}

func (w *fileWalk) innermost(pos token.Pos) (block, bool) {
	var found block
	ok := false
	for _, b := range w.blocks {
		if b.start < pos && pos < b.end {
			if !ok || (b.end-b.start) < (found.end-found.start) {
				found, ok = b, true
			}
		}
	}
	return found, ok
}

func (w *fileWalk) commentBetween(from, to token.Pos) bool {
	for _, group := range w.groups {
		if group.Pos() > from && group.End() < to {
			return true
		}
	}
	return false
}

func (w *fileWalk) context(group *ast.CommentGroup, code core.Span) string {
	scope, ok := w.enclosing(group.Pos(), code)
	if !ok {
		return ""
	}
	return extract.Context(w.src, core.Span{Start: w.offset(scope.start), End: w.offset(scope.end)}, code, w.commentSpans, w.options.ContextLines)
}

func (w *fileWalk) enclosing(pos token.Pos, code core.Span) (owner, bool) {
	var found owner
	ok := false
	for _, scope := range w.scopes {
		if scope.start <= pos && pos <= scope.end && w.offset(scope.start) <= code.Start && code.End <= w.offset(scope.end) {
			if !ok || (scope.end-scope.start) < (found.end-found.start) {
				found, ok = scope, true
			}
		}
	}
	return found, ok
}

func (w *fileWalk) text(start, end token.Pos, maxLines int) (string, core.Span) {
	span := core.Span{Start: w.offset(start), End: w.offset(end)}
	text := string(w.src[span.Start:span.End])
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text, span
	}
	kept := strings.Join(lines[:maxLines], "\n")
	return kept + truncationMarker, core.Span{Start: span.Start, End: span.Start + len(kept)}
}

func (w *fileWalk) offset(pos token.Pos) int { return w.file.Offset(pos) }
func (w *fileWalk) line(pos token.Pos) int   { return w.file.Line(pos) }
