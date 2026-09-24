//go:build cgo

package treesitter

import (
	"regexp"

	sitter "github.com/tree-sitter/go-tree-sitter"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/SergeAx/scrutus/internal/extract"
)

func init() {
	extract.Register(Extractor{&javascriptGrammar})
	extract.Register(Extractor{&typescriptGrammar})
}

var (
	javascriptLanguage = sitter.NewLanguage(javascript.Language())
	typescriptLanguage = sitter.NewLanguage(typescript.LanguageTypescript())
	tsxLanguage        = sitter.NewLanguage(typescript.LanguageTSX())
)

var javascriptGrammar = grammar{
	name:         "javascript",
	extensions:   []string{".js", ".jsx", ".mjs", ".cjs"},
	language:     func(string) *sitter.Language { return javascriptLanguage },
	truncation:   "// ... truncated by scrutus",
	containers:   jsContainers,
	itemField:    jsItemFields,
	clauses:      jsClauses,
	scopes:       jsScopes,
	documentable: jsDocumentable,
	directive:    tripleSlash.MatchString,
}

var typescriptGrammar = grammar{
	name:       "typescript",
	extensions: []string{".ts", ".tsx", ".mts", ".cts"},
	language: func(ext string) *sitter.Language {
		if ext == ".tsx" {
			return tsxLanguage
		}
		return typescriptLanguage
	},
	truncation:   "// ... truncated by scrutus",
	containers:   jsContainers,
	itemField:    jsItemFields,
	clauses:      jsClauses,
	scopes:       jsScopes,
	documentable: jsDocumentable,
	directive:    tripleSlash.MatchString,
}

var jsContainers = set(
	"program", "statement_block", "class_body", "switch_body", "switch_case", "switch_default",
	"object", "array", "arguments", "formal_parameters", "named_imports", "export_clause",
	"interface_body", "object_type", "enum_body",
)

var jsItemFields = map[string]string{"switch_case": "body", "switch_default": "body"}

var jsClauses = set("else_clause", "catch_clause", "finally_clause")

var jsScopes = set(
	"function_declaration", "generator_function_declaration", "function_expression", "function",
	"generator_function", "arrow_function", "method_definition", "class_static_block",
)

var jsDeclarations = set(
	"export_statement", "function_declaration", "generator_function_declaration",
	"class_declaration", "abstract_class_declaration", "method_definition", "field_definition",
	"public_field_definition", "method_signature", "abstract_method_signature",
	"property_signature", "function_signature", "interface_declaration",
	"type_alias_declaration", "enum_declaration", "ambient_declaration", "internal_module", "module",
)

// jsDocumentable keeps a comment above a local variable on the paragraph rule:
// inside a function body it heads the statements below, while at module level
// it documents the binding.
func jsDocumentable(n *sitter.Node, inFunction bool) bool {
	switch n.Kind() {
	case "lexical_declaration", "variable_declaration":
		return !inFunction
	case "expression_statement":
		inner := n.NamedChild(0)
		return inner != nil && (inner.Kind() == "internal_module" || inner.Kind() == "module")
	}
	return jsDeclarations[n.Kind()]
}

// tripleSlash matches TypeScript's `/// <reference … />` compiler directives.
var tripleSlash = regexp.MustCompile(`^///\s*<`)
