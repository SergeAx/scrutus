//go:build cgo

package treesitter

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"

	"github.com/SergeAx/scrutus/internal/extract"
)

func init() { extract.Register(Extractor{&pythonGrammar}) }

var pythonGrammar = grammar{
	name:       "python",
	extensions: []string{".py", ".pyi"},
	language:   func(string) *sitter.Language { return python.GetLanguage() },
	truncation: "# ... truncated by scrutus",
	containers: set("module", "block", "dictionary", "list", "set", "argument_list", "parameters"),
	headed:     set("block"),
	scopes:     set("function_definition"),
	docstring:  pythonDocstring,
}

func pythonDocstring(n *sitter.Node, src []byte) (docstring, bool) {
	decl, body := n, n
	switch n.Type() {
	case "module":
	case "function_definition", "class_definition":
		// Not ChildByFieldName("body"): the runtime files a comment under the
		// header as the body too.
		body = nil
		for _, k := range children(n) {
			if k.node.Type() == "block" {
				body = k.node
			}
		}
		if parent := n.Parent(); parent != nil && parent.Type() == "decorated_definition" {
			decl = parent
		}
	default:
		return docstring{}, false
	}
	if body == nil {
		return docstring{}, false
	}

	var statements []*sitter.Node
	for _, k := range children(body) {
		if k.node.IsNamed() && k.node.Type() != "comment" {
			statements = append(statements, k.node)
		}
	}
	if len(statements) == 0 || statements[0].Type() != "expression_statement" || statements[0].NamedChildCount() != 1 {
		return docstring{}, false
	}
	str := statements[0].NamedChild(0)
	if str.Type() != "string" {
		return docstring{}, false
	}
	parts := children(str)
	if len(parts) < 2 || parts[0].node.Type() != "string_start" || parts[len(parts)-1].node.Type() != "string_end" {
		return docstring{}, false
	}
	open, closing := parts[0].node, parts[len(parts)-1].node
	// An f-string or a bytes literal in first position is not a docstring:
	// Python leaves __doc__ unset for both.
	if strings.ContainsAny(open.Content(src), "fFbB") {
		return docstring{}, false
	}

	return docstring{
		stmt: spanOf(statements[0]),
		text: span{int(open.EndByte()), int(closing.StartByte())},
		decl: spanOf(decl),
		sole: n.Type() != "module" && len(statements) == 1,
	}, true
}
