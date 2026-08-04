// The symbol-backend evaluation harness is a separate module.
//
// It links tree-sitter through cgo, which the portable host executable must never
// do, and it exists only to produce evidence for ADR-0019. A nested module keeps
// it out of `go build ./...` on the developer's machine, the same boundary
// ADR-0011 established for the search engine.
module github.com/lev-goryachev/lctk/spikes/symbol-backend-evaluation

go 1.25.9

require (
	github.com/tree-sitter/go-tree-sitter v0.25.0
	github.com/tree-sitter/tree-sitter-c v0.24.2
	github.com/tree-sitter/tree-sitter-cpp v0.23.4
	github.com/tree-sitter/tree-sitter-go v0.25.0
	github.com/tree-sitter/tree-sitter-javascript v0.25.0
	github.com/tree-sitter/tree-sitter-python v0.25.0
	github.com/tree-sitter/tree-sitter-rust v0.24.2
	github.com/tree-sitter/tree-sitter-typescript v0.23.2
)

require github.com/mattn/go-pointer v0.0.1 // indirect
