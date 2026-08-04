// Package symbols extracts declarations from source using Tree-sitter.
//
// It parses bytes and nothing else. Nothing here opens a file, resolves a path,
// or consults the project's ignore rules: the caller supplies content it has
// already decided belongs to the project, which keeps the one component that
// links a C library out of the business of deciding what may be read.
//
// What it produces is what the syntax says, and no more. A declaration's name,
// kind, extent, and enclosing declaration are all taken from the tree. Nothing is
// type-resolved, because nothing here can resolve a type: [ADR-0019] is explicit
// that a cross-file answer is matched by name and is reported as such.
//
// [ADR-0019]: ../../../../docs/adr/0019-tree-sitter-symbol-layer.md
package symbols

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tscpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// SchemaVersion is the version of the shapes this package produces. It is
// reported as provenance so a caller can reason about the contract it is reading.
const SchemaVersion = 1

// Language names. They are LCTK's, not a grammar's, and they are what a caller
// sees.
const (
	LanguageGo         = "go"
	LanguagePython     = "python"
	LanguageRust       = "rust"
	LanguageC          = "c"
	LanguageCPP        = "cpp"
	LanguageJavaScript = "javascript"
	LanguageTypeScript = "typescript"
	LanguageTSX        = "tsx"
)

// noPreprocessorNote explains a withheld syntax verdict for C and C++.
//
// Slice 4.1 measured the reason rather than assuming it: 58 of 69 real files from
// zlib and 134 of 438 from nlohmann/json parse with errors while compiling
// perfectly, because the grammars have no preprocessor. Publishing a verdict would
// report a defect in most of a C project on the first look.
const noPreprocessorNote = "The C and C++ grammars have no preprocessor, so valid code " +
	"often parses with errors. No syntax verdict is published for this language; " +
	"use the project's own build or lint command instead."

// DefaultBudget bounds one file's parse.
//
// The cost of a parse is not proportional to size — a pathological construct in a
// file of ordinary length is what holds a parser — so the bound is wall clock. The
// container it would otherwise hold is the project's own, which is the reason a
// bound exists at all rather than a reason to make it generous.
const DefaultBudget = 5 * time.Second

// DefaultMaxBytes bounds the file a caller may ask about.
//
// It is deliberately larger than the index's own file limit. The two limits exist
// for different reasons: a large file costs the index space in every generation it
// appears in, while an outline costs one parse and is not stored, so refusing a
// 1.5 MB generated declaration file would decline an answer nobody has to keep.
const DefaultMaxBytes = 4 << 20

// Kind is the normalized declaration category.
//
// The vocabulary is LCTK's so that a caller does not have to learn one grammar's
// node names, and it is deliberately small: a category no configured grammar can
// produce is not in it.
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

// Symbol is one declaration.
type Symbol struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// StartLine and EndLine are 1-based and inclusive.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	// StartByte and EndByte bound the declaration in the file. They are what makes
	// "show me this declaration" answerable without a second guess about where it
	// ends, and they are what a chunk model will need.
	StartByte int `json:"start_byte"`
	EndByte   int `json:"end_byte"`
	// Container is the enclosing declaration's name, empty at the top level. It is
	// computed from the tree's extents rather than from a name path, so it is exact:
	// a declaration is inside another when its bytes are.
	Container string `json:"container,omitempty"`
	// Depth is how deeply the declaration is nested, so a flat list can be rendered
	// as a tree without recomputing containment.
	Depth int `json:"depth"`
	// Signature is the declaration's own first line, trimmed and bounded. It is here
	// because it is what a reader actually wants next, and returning it costs
	// nothing once the extent is known.
	Signature string `json:"signature,omitempty"`
}

// maxSignatureBytes bounds the first line carried back. A minified or generated
// file can have a single line of any length, and a caller asked for an outline.
const maxSignatureBytes = 200

// Syntax is what the parser can say about the file being whole.
type Syntax struct {
	// Reported says a verdict is published for this language at all. It is false
	// for languages where the grammar's opinion is not trustworthy, and a caller
	// must read false as "unknown" rather than as "fine".
	Reported bool `json:"reported"`
	Valid    bool `json:"valid"`
	// Errors counts the broken regions located, not the nodes inside them: the
	// number of places to look, rather than the size of the wreckage.
	Errors int `json:"errors,omitempty"`
	// FirstErrorLine is where to look first.
	FirstErrorLine int `json:"first_error_line,omitempty"`
	// Note explains a withheld verdict, so "reported: false" is not a silence a
	// caller has to interpret.
	Note string `json:"note,omitempty"`
}

// Outline is one file's declarations.
type Outline struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Bytes    int    `json:"bytes"`
	Lines    int    `json:"lines"`
	// Digest is the content this answer describes, so a caller can tell whether two
	// answers are about the same bytes.
	Digest        string   `json:"digest,omitempty"`
	SchemaVersion int      `json:"schema_version"`
	Symbols       []Symbol `json:"symbols"`
	Syntax        Syntax   `json:"syntax"`
}

// grammar is one configured language.
type grammar struct {
	language *ts.Language
	query    *ts.Query
	// identifiers captures every node the grammar calls an identifier, which is what
	// separates an occurrence from the same letters in a comment or a string.
	identifiers *ts.Query
	// reportsSyntax says whether this grammar's opinion about a file being whole is
	// worth publishing. Slice 4.1 measured why this is per language rather than
	// global: the C and C++ grammars have no preprocessor, so most real files in
	// those languages parse with errors while compiling perfectly.
	reportsSyntax bool
	// syntaxNote explains a withheld verdict in the answer itself.
	syntaxNote string
}

// Engine parses and extracts. It is safe for concurrent use.
type Engine struct {
	grammars map[string]*grammar
	// parsers is a pool because a Tree-sitter parser is not safe for concurrent
	// use and creating one per request is measurable next to parsing a small file.
	parsers sync.Pool

	// Budget bounds one file's parse. Zero means unbounded, which no caller should
	// choose in a service.
	Budget time.Duration
	// MaxFileBytes bounds the file a caller may ask about. Zero means unlimited.
	MaxFileBytes int64
}

// MaxBytes is the largest file this engine will outline.
func (e *Engine) MaxBytes() int64 { return e.MaxFileBytes }

// New loads every configured grammar and compiles every query.
//
// Compiling here rather than per file is not only cheaper. A query that does not
// compile against the grammar actually loaded is a configuration error, and it has
// to surface once at startup instead of as an empty answer about some file nobody
// was watching.
func New() (*Engine, error) {
	engine := &Engine{
		grammars:     map[string]*grammar{},
		Budget:       DefaultBudget,
		MaxFileBytes: DefaultMaxBytes,
	}
	engine.parsers.New = func() any { return ts.NewParser() }

	for _, configured := range configuredLanguages {
		language := configured.grammar()
		text := configured.query
		for _, optional := range configured.optionalPatterns {
			// A pattern is appended only if it compiles against this grammar
			// release. A query naming a node the grammar does not have fails to
			// compile rather than matching nothing, so a node renamed between
			// releases has to be discovered rather than assumed.
			probe, err := ts.NewQuery(language, optional)
			if err != nil {
				continue
			}
			probe.Close()
			text += optional + "\n"
			break
		}
		query, err := ts.NewQuery(language, text)
		if err != nil {
			return nil, fmt.Errorf("compile the %s symbol query: %w", configured.name, err)
		}
		identifiers, err := ts.NewQuery(language, configured.identifiers)
		if err != nil {
			query.Close()
			return nil, fmt.Errorf("compile the %s identifier query: %w", configured.name, err)
		}
		engine.grammars[configured.name] = &grammar{
			language:      language,
			query:         query,
			identifiers:   identifiers,
			reportsSyntax: configured.reportsSyntax,
			syntaxNote:    configured.syntaxNote,
		}
	}
	return engine, nil
}

// Close releases the compiled queries.
func (e *Engine) Close() {
	for _, g := range e.grammars {
		g.query.Close()
		g.identifiers.Close()
	}
}

// Languages names what this build can outline, sorted.
func (e *Engine) Languages() []string {
	names := make([]string, 0, len(e.grammars))
	for name := range e.grammars {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LanguageOf reports the configured language for a path, and false when there is
// none.
//
// A language with no grammar configured is explicitly unsupported rather than
// answered less precisely. That is the same rule the rest of this service follows
// about incomplete knowledge: a stated gap is worth more to a caller than a
// confident guess.
func (e *Engine) LanguageOf(name string) (string, bool) {
	language, known := languageByExtension[strings.ToLower(path.Ext(name))]
	if !known {
		return "", false
	}
	if _, configured := e.grammars[language]; !configured {
		return "", false
	}
	return language, true
}

// Outline extracts one file's declarations from its content.
func (e *Engine) Outline(name string, content []byte, digest string) (Outline, error) {
	language, known := e.LanguageOf(name)
	if !known {
		return Outline{}, fail(CodeUnsupportedLanguage,
			fmt.Sprintf("Outlines are not available for %q; this build understands %s.",
				path.Ext(name), strings.Join(e.Languages(), ", ")), false, nil)
	}
	g := e.grammars[language]

	parser := e.parsers.Get().(*ts.Parser)
	defer e.parsers.Put(parser)
	if err := parser.SetLanguage(g.language); err != nil {
		return Outline{}, fail(CodeInternalError, "The parser could not be prepared.", false, err)
	}

	tree := e.parse(parser, content)
	if tree == nil {
		// A cancelled parse and a failed one are indistinguishable through the
		// engine: both yield no tree. With a budget set, the budget is the
		// explanation worth giving, because it is the one an operator can change.
		if e.Budget > 0 {
			return Outline{}, fail(CodeParseIncomplete,
				fmt.Sprintf("The file was not fully parsed within %s.", e.Budget), false, nil)
		}
		return Outline{}, fail(CodeInternalError, "The file could not be parsed.", false, nil)
	}
	defer tree.Close()

	root := tree.RootNode()
	outline := Outline{
		Path:          name,
		Language:      language,
		Bytes:         len(content),
		Lines:         countLines(content),
		Digest:        digest,
		SchemaVersion: SchemaVersion,
		Symbols:       []Symbol{},
		Syntax:        Syntax{Reported: g.reportsSyntax, Note: g.syntaxNote},
	}
	if g.reportsSyntax {
		errors, firstLine := describeErrors(root)
		outline.Syntax.Valid = errors == 0
		outline.Syntax.Errors = errors
		outline.Syntax.FirstErrorLine = firstLine
	}

	outline.Symbols = symbolsOf(nest(e.capture(g, root, content)))
	return outline, nil
}

// byteRange is a half-open extent in a file.
type byteRange struct{ start, end int }

// captured is one query match resolved into a symbol, plus where its name sits and
// whether that name node is a declaration.
type captured struct {
	symbol Symbol
	nameAt byteRange
	// pattern is the index of the query pattern that matched, used to settle which of
	// two patterns describing one declaration decides its kind.
	pattern uint
	// declares says the name node is where this name is introduced. A scope capture
	// nests but declares nothing: a Rust `impl Config` block gives methods inside it
	// a container, while the word `Config` there is a use of a type declared
	// elsewhere. Reporting it as a second declaration of Config would be wrong.
	declares bool
}

// capture runs the language's symbol query and resolves every match.
//
// It is shared by the outline and the lookup because they need the same list for
// different reasons -- one to nest, one to decide what counts as a declaration --
// and two walks that must agree would eventually not.
func (e *Engine) capture(g *grammar, root *ts.Node, content []byte) []captured {
	cursor := ts.NewQueryCursor()
	defer cursor.Close()
	names := g.query.CaptureNames()
	matches := cursor.Matches(g.query, root, content)

	var found []captured
	// at maps a declaration's name extent to its entry in found.
	//
	// Two patterns matching one declaration is normal and useful: in JavaScript a
	// variable_declarator holds a function when its value is one and a plain binding
	// otherwise, and both patterns match. Something has to decide, and the rule is
	// that the first pattern listed in the query wins -- which is why the specific
	// patterns are written before the general ones. Without it, one declaration would
	// appear twice with two different kinds.
	at := map[byteRange]int{}

	for match := matches.Next(); match != nil; match = matches.Next() {
		var name, def *ts.Node
		kind, declares := Kind(""), false
		for index := range match.Captures {
			capture := match.Captures[index]
			role, suffix := splitCapture(names[capture.Index])
			switch role {
			case "name":
				name = &capture.Node
			case "def", "scope":
				def = &capture.Node
				declares = role == "def"
				kind = Kind(suffix)
			}
		}
		if name == nil || def == nil {
			continue
		}
		if kind == "" {
			kind = kindOf(def.Kind())
		}
		nameAt := byteRange{int(name.StartByte()), int(name.EndByte())}
		entry := captured{
			symbol: Symbol{
				Name:      name.Utf8Text(content),
				Kind:      kind,
				StartLine: int(def.StartPosition().Row) + 1,
				EndLine:   int(def.EndPosition().Row) + 1,
				StartByte: int(def.StartByte()),
				EndByte:   int(def.EndByte()),
				Signature: firstLine(content, int(def.StartByte()), int(def.EndByte())),
			},
			nameAt:   nameAt,
			declares: declares,
			pattern:  match.PatternIndex,
		}

		if index, already := at[nameAt]; already {
			// The cursor does not promise to yield matches in pattern order, so the
			// winner is chosen by pattern index rather than by arrival.
			if entry.pattern < found[index].pattern {
				found[index] = entry
			}
			continue
		}
		at[nameAt] = len(found)
		found = append(found, entry)
	}
	return found
}

// splitCapture reads a capture name of the form role or role.kind.
//
// The suffix exists because one grammar node can be two things: in JavaScript a
// variable_declarator holds a function when its value is one and a plain binding
// otherwise, and the node type alone cannot say which. Naming the kind in the
// query is more honest than a table that has to guess.
func splitCapture(name string) (role, suffix string) {
	if index := strings.IndexByte(name, '.'); index >= 0 {
		return name[:index], name[index+1:]
	}
	return name, ""
}

func symbolsOf(found []captured) []Symbol {
	symbols := make([]Symbol, 0, len(found))
	for _, entry := range found {
		symbols = append(symbols, entry.symbol)
	}
	return symbols
}

// parse runs the parser, bounded when a budget is set.
func (e *Engine) parse(parser *ts.Parser, content []byte) *ts.Tree {
	if e.Budget <= 0 {
		return parser.Parse(content, nil)
	}
	deadline := time.Now().Add(e.Budget)
	read := func(offset int, _ ts.Point) []byte {
		if offset >= len(content) {
			return nil
		}
		return content[offset:]
	}
	return parser.ParseWithOptions(read, nil, &ts.ParseOptions{
		ProgressCallback: func(ts.ParseState) bool { return time.Now().After(deadline) },
	})
}

func kindOf(node string) Kind {
	if kind, known := kindByNode[node]; known {
		return kind
	}
	return KindOther
}

// describeErrors counts broken regions and reports the first line to look at.
//
// The walk stops descending at an error, because the nodes inside one are the
// parser's recovery attempt: counting them would report how much wreckage there is
// rather than how many places need attention.
func describeErrors(node *ts.Node) (int, int) {
	if node == nil {
		return 0, 0
	}
	if node.IsError() || node.IsMissing() {
		return 1, int(node.StartPosition().Row) + 1
	}
	if !node.HasError() {
		return 0, 0
	}
	total, first := 0, 0
	for index := uint(0); index < node.ChildCount(); index++ {
		count, line := describeErrors(node.Child(index))
		total += count
		if line != 0 && (first == 0 || line < first) {
			first = line
		}
	}
	return total, first
}

// nest fills in each symbol's container and depth from the byte ranges.
func nest(found []captured) []captured {
	if len(found) == 0 {
		return found
	}
	sort.SliceStable(found, func(a, b int) bool {
		if found[a].symbol.StartByte != found[b].symbol.StartByte {
			return found[a].symbol.StartByte < found[b].symbol.StartByte
		}
		// The wider declaration comes first, so a field never becomes the parent of
		// the type that contains it.
		return found[a].symbol.EndByte > found[b].symbol.EndByte
	})
	var stack []Symbol
	for index := range found {
		for len(stack) > 0 && found[index].symbol.StartByte >= stack[len(stack)-1].EndByte {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			found[index].symbol.Container = stack[len(stack)-1].Name
		}
		found[index].symbol.Depth = len(stack)
		stack = append(stack, found[index].symbol)
	}
	return found
}

// firstLine returns the declaration's own opening line, trimmed and bounded.
func firstLine(content []byte, start, end int) string {
	if start < 0 || end > len(content) || start >= end {
		return ""
	}
	segment := content[start:end]
	if index := indexByte(segment, '\n'); index >= 0 {
		segment = segment[:index]
	}
	text := strings.TrimSpace(string(segment))
	if len(text) > maxSignatureBytes {
		// Cut on a rune boundary so the result is still valid UTF-8 when a
		// declaration's opening line is long and non-ASCII.
		cut := maxSignatureBytes
		for cut > 0 && !utf8Start(text[cut]) {
			cut--
		}
		text = strings.TrimRight(text[:cut], " \t") + "…"
	}
	return text
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func indexByte(data []byte, target byte) int {
	for index, value := range data {
		if value == target {
			return index
		}
	}
	return -1
}

func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := 1
	for _, value := range content {
		if value == '\n' {
			lines++
		}
	}
	// A trailing newline ends the last line rather than starting another.
	if content[len(content)-1] == '\n' {
		lines--
	}
	return lines
}

// configuredLanguages is the set this build understands.
//
// Grammars are added a slice at a time rather than all at once, because a grammar
// without a verified query is a language that answers plausibly and wrongly.
var configuredLanguages = []struct {
	name             string
	grammar          func() *ts.Language
	query            string
	identifiers      string
	optionalPatterns []string
	reportsSyntax    bool
	syntaxNote       string
}{
	{
		name:             LanguageGo,
		grammar:          func() *ts.Language { return ts.NewLanguage(tsgo.Language()) },
		query:            goQuery,
		identifiers:      goIdentifiers,
		optionalPatterns: goInterfaceMethodPatterns,
		reportsSyntax:    true,
	},
	{
		name:          LanguagePython,
		grammar:       func() *ts.Language { return ts.NewLanguage(tspython.Language()) },
		query:         pythonQuery,
		identifiers:   pythonIdentifiers,
		reportsSyntax: true,
	},
	{
		name:          LanguageRust,
		grammar:       func() *ts.Language { return ts.NewLanguage(tsrust.Language()) },
		query:         rustQuery,
		identifiers:   rustIdentifiers,
		reportsSyntax: true,
	},
	{
		name:        LanguageC,
		grammar:     func() *ts.Language { return ts.NewLanguage(tsc.Language()) },
		query:       cQuery,
		identifiers: cIdentifiers,
		// No syntax verdict: see noPreprocessorNote.
		reportsSyntax: false,
		syntaxNote:    noPreprocessorNote,
	},
	{
		name:          LanguageCPP,
		grammar:       func() *ts.Language { return ts.NewLanguage(tscpp.Language()) },
		query:         cppQuery,
		identifiers:   cppIdentifiers,
		reportsSyntax: false,
		syntaxNote:    noPreprocessorNote,
	},
	{
		name:          LanguageJavaScript,
		grammar:       func() *ts.Language { return ts.NewLanguage(tsjs.Language()) },
		query:         javascriptQuery,
		identifiers:   ecmaIdentifiers,
		reportsSyntax: true,
	},
	{
		name:          LanguageTypeScript,
		grammar:       func() *ts.Language { return ts.NewLanguage(tsts.LanguageTypescript()) },
		query:         typescriptQuery,
		identifiers:   typescriptIdentifiers,
		reportsSyntax: true,
	},
	{
		// TSX is a separate grammar rather than a dialect: the two disagree about
		// how `<T>` is read, so one grammar cannot serve both.
		name:          LanguageTSX,
		grammar:       func() *ts.Language { return ts.NewLanguage(tsts.LanguageTSX()) },
		query:         typescriptQuery,
		identifiers:   typescriptIdentifiers,
		reportsSyntax: true,
	},
}

// languageByExtension is LCTK's own extension map. It lists every extension the
// project may eventually understand; LanguageOf then refuses one whose grammar is
// not configured in this build, so an extension here is not a promise.
//
// A `.h` is read as C rather than C++. The extension genuinely does not say which,
// and C is the reading that parses a C header correctly; a C++ header in a `.h`
// yields errors, which is why no syntax verdict is published for either language.
var languageByExtension = map[string]string{
	".go":  LanguageGo,
	".py":  LanguagePython,
	".pyi": LanguagePython,
	".rs":  LanguageRust,
	".c":   LanguageC,
	".h":   LanguageC,
	".cc":  LanguageCPP,
	".cpp": LanguageCPP,
	".cxx": LanguageCPP,
	".hh":  LanguageCPP,
	".hpp": LanguageCPP,
	".hxx": LanguageCPP,
	".js":  LanguageJavaScript,
	".mjs": LanguageJavaScript,
	".cjs": LanguageJavaScript,
	".jsx": LanguageJavaScript,
	// A `.d.ts` is matched by `.ts`: path.Ext returns only the last extension.
	".ts":  LanguageTypeScript,
	".mts": LanguageTypeScript,
	".cts": LanguageTypeScript,
	".tsx": LanguageTSX,
}
