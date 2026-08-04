// Package engines normalizes two candidate symbol extractors onto one shape so
// the same corpus can be measured through both.
//
// Normalization is deliberately shallow. Anything the harness computes for a
// candidate that the candidate does not itself provide would credit the
// candidate with a capability LCTK would have to write, which is the mistake
// the search-backend evaluation contract calls out for adapters.
package engines

import "time"

// Kind is the normalized symbol category.
//
// Both candidates emit their own vocabulary, and the point of naming a small set
// is that a caller of the future tool should not have to learn one engine's
// words. A category neither engine can produce is not in the set.
type Kind string

const (
	KindFunction  Kind = "function"
	KindMethod    Kind = "method"
	KindType      Kind = "type"
	KindInterface Kind = "interface"
	KindStruct    Kind = "struct"
	KindEnum      Kind = "enum"
	KindClass     Kind = "class"
	KindField     Kind = "field"
	KindConstant  Kind = "constant"
	KindVariable  Kind = "variable"
	KindModule    Kind = "module"
	KindMacro     Kind = "macro"
	KindOther     Kind = "other"
)

// Symbol is one declaration found in one file.
type Symbol struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// StartLine and EndLine are 1-based and inclusive. An engine that cannot say
	// where a declaration ends reports EndLine equal to StartLine, and the harness
	// records that separately rather than hiding it.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	// StartByte and EndByte bound the declaration in the file. They are what a
	// future AST-aware chunk model needs, and an engine that cannot produce them
	// reports zero.
	StartByte int `json:"start_byte"`
	EndByte   int `json:"end_byte"`
	// Container is the enclosing declaration's name, empty at the top level.
	Container string `json:"container,omitempty"`
}

// FileResult is one file's analysis.
type FileResult struct {
	Path     string   `json:"path"`
	Language string   `json:"language"`
	Bytes    int      `json:"bytes"`
	Symbols  []Symbol `json:"symbols"`
	// Parsed reports whether the engine considers the file syntactically whole.
	// An engine with no notion of failure reports true always, which is not the
	// same claim and is recorded as unsupported rather than as a pass.
	Parsed bool `json:"parsed"`
	// ParseErrors counts the syntactically broken regions the engine located.
	ParseErrors int `json:"parse_errors"`
	// TimedOut says the engine was stopped before it finished this file. It is
	// separate from Err because it is not a failure of the engine: it is the
	// budget doing its job, and the honest report is "no answer for this file"
	// rather than "this file has no symbols".
	TimedOut bool `json:"timed_out,omitempty"`
	// Err is set when the engine could not analyse the file at all.
	Err string `json:"error,omitempty"`
}

// Capabilities is what an engine claims it can do, declared rather than inferred
// from a run. A capability the harness cannot then demonstrate is a failed gate,
// not a rounding error.
type Capabilities struct {
	// ByteRanges says the engine bounds a declaration in bytes, not only in lines.
	ByteRanges bool
	// Containment says the engine reports the enclosing declaration.
	Containment bool
	// SyntaxValidity says the engine can report that a file does not parse.
	SyntaxValidity bool
	// InProcess says the engine needs no subprocess and no external executable.
	InProcess bool
	// License is the distribution licence of what would ship.
	License string
}

// Request is one file offered to an engine.
//
// Both the content and a path to it are supplied because the candidates need
// different things, and that difference is itself a finding: tree-sitter parses
// bytes and never touches the filesystem, while Universal Ctags cannot be given
// source on standard input and requires a path it can open. In LCTK the file is on
// the read-only mount either way, so supplying the path is fair rather than
// generous.
type Request struct {
	// Path is how the file is named in reports, normally project-relative.
	Path string
	// Full is a path the engine may open. It is empty when the caller has content
	// only, in which case an engine that needs a file must arrange one itself.
	Full string
	// Language is the harness's name for the language, not the engine's.
	Language string
}

// Engine is a candidate symbol extractor.
type Engine interface {
	Name() string
	Capabilities() Capabilities
	// Languages names what the engine is configured to handle here. It is the
	// configured set rather than everything the engine could ever parse, because
	// an unconfigured language answers nothing.
	Languages() []string
	// Analyse returns one file's declarations.
	Analyse(request Request, content []byte) FileResult
	Close()
}

// Timing is what a run cost.
type Timing struct {
	Files    int           `json:"files"`
	Bytes    int64         `json:"bytes"`
	Duration time.Duration `json:"duration_ns"`
}
