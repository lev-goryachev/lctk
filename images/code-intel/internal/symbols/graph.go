package symbols

import (
	"context"
	"fmt"
	"sort"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// GraphDeclaration is one declaration persisted as derived graph evidence. It
// carries syntax coordinates but no inferred type or globally resolved identity,
// because the Stage 6 graph promises name matching and nothing stronger.
type GraphDeclaration struct {
	Name      string `json:"name"`
	Kind      Kind   `json:"kind"`
	Container string `json:"container,omitempty"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
	Signature string `json:"signature,omitempty"`
}

// GraphImport is one import-shaped syntax node. Target is normalized source
// text, not a claim that a compiler or package manager resolved it.
type GraphImport struct {
	Target string `json:"target"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// GraphCall is one called identifier and its innermost declaration.
type GraphCall struct {
	Caller string `json:"caller,omitempty"`
	Callee string `json:"callee"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// GraphFacts is all graph evidence derived from one saved file.
type GraphFacts struct {
	Path         string             `json:"path"`
	Language     string             `json:"language"`
	Digest       string             `json:"digest"`
	Declarations []GraphDeclaration `json:"declarations"`
	Imports      []GraphImport      `json:"imports"`
	Calls        []GraphCall        `json:"calls"`
}

// Facts extracts declarations, imports, and calls from one Tree-sitter tree.
// Unsupported text contributes semantic chunks but no fabricated graph edges.
func (e *Engine) Facts(ctx context.Context, name string, content []byte, digest string) (GraphFacts, error) {
	language, known := e.LanguageOf(name)
	empty := GraphFacts{Path: name, Digest: digest, Declarations: []GraphDeclaration{}, Imports: []GraphImport{}, Calls: []GraphCall{}}
	if !known {
		return empty, nil
	}
	g := e.grammars[language]
	if err := e.acquire(ctx); err != nil {
		return GraphFacts{}, err
	}
	defer e.release()
	parser := e.parsers.Get().(*ts.Parser)
	defer e.parsers.Put(parser)
	if err := parser.SetLanguage(g.language); err != nil {
		return GraphFacts{}, fail(CodeInternalError, "The graph parser could not be prepared.", false, err)
	}
	tree := e.parse(parser, content)
	if tree == nil {
		return GraphFacts{}, fail(CodeParseIncomplete,
			fmt.Sprintf("The file graph was not fully parsed within %s.", e.Budget), false, nil)
	}
	defer tree.Close()

	root := tree.RootNode()
	captured := nest(e.capture(g, root, content))
	facts := empty
	facts.Language = language
	declarations := make([]Symbol, 0, len(captured))
	for _, item := range captured {
		symbol := item.symbol
		declarations = append(declarations, symbol)
		facts.Declarations = append(facts.Declarations, GraphDeclaration{
			Name: symbol.Name, Kind: symbol.Kind, Container: symbol.Container,
			Line: symbol.StartLine, Column: 1, StartByte: symbol.StartByte,
			EndByte: symbol.EndByte, Signature: symbol.Signature,
		})
	}

	cursor := ts.NewQueryCursor()
	defer cursor.Close()
	captureNames := g.graph.CaptureNames()
	matches := cursor.Matches(g.graph, root, content)
	for match := matches.Next(); match != nil; match = matches.Next() {
		for _, capture := range match.Captures {
			node := &capture.Node
			switch captureNames[capture.Index] {
			case "call":
				callee := rightmostIdentifier(node, content)
				if callee != "" {
					facts.Calls = append(facts.Calls, GraphCall{
						Caller: innermostContaining(declarations, int(node.StartByte()), int(node.EndByte())),
						Callee: callee, Line: int(node.StartPosition().Row) + 1,
						Column: int(node.StartPosition().Column) + 1,
					})
				}
			case "import":
				for _, target := range importTargets(language, node.Utf8Text(content)) {
					facts.Imports = append(facts.Imports, GraphImport{
						Target: target, Line: int(node.StartPosition().Row) + 1,
						Column: int(node.StartPosition().Column) + 1,
					})
				}
			}
		}
	}
	dedupeGraphFacts(&facts)
	return facts, nil
}

func rightmostIdentifier(node *ts.Node, content []byte) string {
	if node == nil {
		return ""
	}
	for index := node.NamedChildCount(); index > 0; index-- {
		if value := rightmostIdentifier(node.NamedChild(index-1), content); value != "" {
			return value
		}
	}
	if strings.Contains(node.Kind(), "identifier") {
		return strings.TrimSpace(node.Utf8Text(content))
	}
	return ""
}

// importTargets operates only on nodes a language grammar identified as imports.
// Small textual normalization inside that trusted boundary covers aliases and
// multiple Python imports without treating comments as dependencies.
func importTargets(language, statement string) []string {
	statement = strings.TrimSpace(statement)
	if quoted := quotedValues(statement); len(quoted) > 0 {
		return quoted
	}
	switch language {
	case LanguagePython:
		value := strings.TrimPrefix(statement, "from ")
		if index := strings.Index(value, " import "); index >= 0 {
			module := strings.TrimSpace(value[:index])
			if strings.Trim(module, ".") == "" {
				imported := strings.TrimSpace(strings.SplitN(value[index+8:], " as ", 2)[0])
				module += imported
			}
			return []string{module}
		}
		value = strings.TrimPrefix(value, "import ")
		var result []string
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(strings.SplitN(part, " as ", 2)[0])
			if part != "" {
				result = append(result, part)
			}
		}
		return result
	case LanguageRust:
		value := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(statement, "use "), ";"))
		value = strings.TrimPrefix(value, "crate::")
		if index := strings.IndexAny(value, ":{"); index >= 0 {
			value = value[:index]
		}
		if value != "" {
			return []string{value}
		}
	}
	return nil
}

func quotedValues(value string) []string {
	var result []string
	for index := 0; index < len(value); index++ {
		quote := value[index]
		if quote != '\'' && quote != '"' && quote != '`' && quote != '<' {
			continue
		}
		closing := quote
		if quote == '<' {
			closing = '>'
		}
		start := index + 1
		for index = start; index < len(value); index++ {
			if value[index] == closing && (index == start || value[index-1] != '\\') {
				if target := strings.TrimSpace(value[start:index]); target != "" {
					result = append(result, target)
				}
				break
			}
		}
	}
	return result
}

func dedupeGraphFacts(facts *GraphFacts) {
	seenCalls := map[string]bool{}
	calls := facts.Calls[:0]
	for _, call := range facts.Calls {
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%d", call.Caller, call.Callee, call.Line, call.Column)
		if !seenCalls[key] {
			seenCalls[key] = true
			calls = append(calls, call)
		}
	}
	facts.Calls = calls
	seenImports := map[string]bool{}
	imports := facts.Imports[:0]
	for _, imported := range facts.Imports {
		key := fmt.Sprintf("%s\x00%d\x00%d", imported.Target, imported.Line, imported.Column)
		if !seenImports[key] {
			seenImports[key] = true
			imports = append(imports, imported)
		}
	}
	facts.Imports = imports
	sort.Slice(facts.Imports, func(i, j int) bool {
		if facts.Imports[i].Line != facts.Imports[j].Line {
			return facts.Imports[i].Line < facts.Imports[j].Line
		}
		return facts.Imports[i].Target < facts.Imports[j].Target
	})
}
