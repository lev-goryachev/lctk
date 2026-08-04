package engines

import (
	"fmt"
	"sort"
	"time"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tscpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspy "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// TreeSitter extracts symbols from a real syntax tree.
type TreeSitter struct {
	parser  *ts.Parser
	grammar map[string]*grammar
	// Budget bounds one file's parse. Zero means unbounded, which is what the
	// harness measured first and is recorded in the results as a hazard rather
	// than as a default: a generated or adversarial file can hold a parser for
	// minutes, and a project's own container is where that cost lands.
	Budget time.Duration
}

type grammar struct {
	language *ts.Language
	query    *ts.Query
}

// NewTreeSitter loads every grammar and compiles every query up front.
//
// Compiling at construction rather than per file is not only cheaper: a query
// that does not compile against the grammar actually loaded is a configuration
// error, and it should surface once at startup rather than as an empty answer on
// some file nobody was watching.
func NewTreeSitter() (*TreeSitter, error) {
	engine := &TreeSitter{parser: ts.NewParser(), grammar: map[string]*grammar{}}

	sources := []struct {
		language string
		lang     *ts.Language
		query    string
		extra    []string
	}{
		{"go", ts.NewLanguage(tsgo.Language()), goQuery, goInterfaceMethodQueries},
		{"python", ts.NewLanguage(tspy.Language()), pythonQuery, nil},
		{"rust", ts.NewLanguage(tsrust.Language()), rustQuery, nil},
		{"c", ts.NewLanguage(tsc.Language()), cQuery, nil},
		{"cpp", ts.NewLanguage(tscpp.Language()), cppQuery, nil},
		{"javascript", ts.NewLanguage(tsjs.Language()), javascriptQuery, nil},
		{"typescript", ts.NewLanguage(tsts.LanguageTypescript()), typescriptQuery, nil},
		{"tsx", ts.NewLanguage(tsts.LanguageTSX()), typescriptQuery, nil},
	}

	for _, source := range sources {
		text := source.query
		// An optional pattern is appended only if it compiles against this grammar
		// release. See goInterfaceMethodQueries.
		for _, candidate := range source.extra {
			probe, err := ts.NewQuery(source.lang, candidate)
			if err != nil {
				continue
			}
			probe.Close()
			text += candidate + "\n"
			break
		}
		query, err := ts.NewQuery(source.lang, text)
		if err != nil {
			return nil, fmt.Errorf("compile the %s symbol query: %w", source.language, err)
		}
		engine.grammar[source.language] = &grammar{language: source.lang, query: query}
	}
	return engine, nil
}

func (t *TreeSitter) Name() string { return "tree-sitter" }

func (t *TreeSitter) Capabilities() Capabilities {
	return Capabilities{
		ByteRanges:     true,
		Containment:    true,
		SyntaxValidity: true,
		InProcess:      true,
		License:        "MIT (core and grammars)",
	}
}

func (t *TreeSitter) Languages() []string {
	names := make([]string, 0, len(t.grammar))
	for name := range t.grammar {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (t *TreeSitter) Close() {
	for _, g := range t.grammar {
		g.query.Close()
	}
	t.parser.Close()
}

func (t *TreeSitter) Analyse(request Request, content []byte) FileResult {
	result := FileResult{Path: request.Path, Language: request.Language, Bytes: len(content)}
	g, known := t.grammar[request.Language]
	if !known {
		result.Err = "no grammar configured for " + request.Language
		return result
	}
	if err := t.parser.SetLanguage(g.language); err != nil {
		result.Err = err.Error()
		return result
	}
	tree := t.parse(content)
	if tree == nil {
		// A cancelled parse and a failed one are indistinguishable in the C API:
		// both return no tree. With a budget set, the budget is the explanation
		// worth reporting, because it is the one the operator can change.
		if t.Budget > 0 {
			result.TimedOut = true
			return result
		}
		result.Err = "the parser returned no tree"
		return result
	}
	defer tree.Close()

	root := tree.RootNode()
	result.Parsed = !root.HasError()
	result.ParseErrors = countErrors(root)

	cursor := ts.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(g.query, root, content)

	var found []Symbol
	for match := matches.Next(); match != nil; match = matches.Next() {
		var name, def *ts.Node
		for index := range match.Captures {
			capture := match.Captures[index]
			switch g.query.CaptureNames()[capture.Index] {
			case "name":
				name = &capture.Node
			case "def":
				def = &capture.Node
			}
		}
		if name == nil || def == nil {
			continue
		}
		found = append(found, Symbol{
			Name:      name.Utf8Text(content),
			Kind:      kindOf(def.Kind()),
			StartLine: int(def.StartPosition().Row) + 1,
			EndLine:   int(def.EndPosition().Row) + 1,
			StartByte: int(def.StartByte()),
			EndByte:   int(def.EndByte()),
		})
	}
	result.Symbols = nest(found)
	return result
}

// parse runs the parser, bounded when a budget is set.
//
// The bound is wall clock rather than a byte count, because the cost is not
// proportional to size: the pathological cases are deeply ambiguous constructs in
// a file of ordinary length, and a size limit would let those through while
// refusing large files that parse instantly.
func (t *TreeSitter) parse(content []byte) *ts.Tree {
	if t.Budget <= 0 {
		return t.parser.Parse(content, nil)
	}
	deadline := time.Now().Add(t.Budget)
	read := func(offset int, _ ts.Point) []byte {
		if offset >= len(content) {
			return nil
		}
		return content[offset:]
	}
	return t.parser.ParseWithOptions(read, nil, &ts.ParseOptions{
		ProgressCallback: func(ts.ParseState) bool { return time.Now().After(deadline) },
	})
}

func kindOf(node string) Kind {
	if kind, known := kindByNode[node]; known {
		return kind
	}
	return KindOther
}

// countErrors reports how many broken regions the tree holds.
//
// The walk stops descending at an error, because the nodes inside one are the
// parser's best effort at recovery and counting them would report the size of the
// wreckage rather than the number of places to look.
func countErrors(node *ts.Node) int {
	if node == nil {
		return 0
	}
	if node.IsError() || node.IsMissing() {
		return 1
	}
	if !node.HasError() {
		return 0
	}
	total := 0
	for index := uint(0); index < node.ChildCount(); index++ {
		total += countErrors(node.Child(index))
	}
	return total
}

// nest fills in each symbol's container from the byte ranges.
//
// Containment is computed from the tree's own extents rather than from a name
// path, so it is exact: a declaration is inside another when its bytes are.
func nest(symbols []Symbol) []Symbol {
	sort.SliceStable(symbols, func(a, b int) bool {
		if symbols[a].StartByte != symbols[b].StartByte {
			return symbols[a].StartByte < symbols[b].StartByte
		}
		return symbols[a].EndByte > symbols[b].EndByte
	})
	var stack []Symbol
	for index := range symbols {
		for len(stack) > 0 && symbols[index].StartByte >= stack[len(stack)-1].EndByte {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			symbols[index].Container = stack[len(stack)-1].Name
		}
		stack = append(stack, symbols[index])
	}
	return symbols
}
