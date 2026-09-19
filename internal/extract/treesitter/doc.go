// Package treesitter extracts comment/code pairs from JavaScript and
// TypeScript sources with tree-sitter grammars. The grammars are C, so the
// extractors register only in a cgo build; without cgo the package is empty and
// extract reports these languages as missing instead of skipping their files.
package treesitter
