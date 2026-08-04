package symbols

import (
	"sort"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// maxPreviewBytes bounds one returned source line.
const maxPreviewBytes = 320

// Occurrence is one place a name appears as an identifier.
//
// "As an identifier" is the whole value of this over a text search. The same
// letters inside a comment, a string, or a longer word are not occurrences, and a
// caller acting on them would be acting on prose.
type Occurrence struct {
	Line   int `json:"line"`
	Column int `json:"column"`
	// StartByte and EndByte bound the identifier itself.
	StartByte int `json:"start_byte"`
	EndByte   int `json:"end_byte"`
	// Declaration says this is where the name is declared rather than used. It is
	// the distinction a caller needs first, and it is decided by the syntax rather
	// than by a heuristic about the surrounding line.
	Declaration bool `json:"declaration"`
	// Kind is the declaration's kind, set only when Declaration is true.
	Kind Kind `json:"kind,omitempty"`
	// Container is the enclosing declaration's name, so an occurrence can be placed
	// without opening the file.
	Container string `json:"container,omitempty"`
	// Preview is the source line, trimmed and bounded.
	Preview string `json:"preview,omitempty"`
}

// Located is one file's occurrences of one name.
type Located struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Digest   string `json:"digest,omitempty"`
	// Occurrences are ordered by position.
	Occurrences []Occurrence `json:"occurrences"`
	// Declarations counts how many of them declare the name, so a caller can tell
	// "used in this file" from "defined in this file" without scanning the list.
	Declarations int `json:"declarations"`
	// Parsed reports whether the file is syntactically whole, for the languages
	// where that verdict is published. A file that does not parse still yields
	// occurrences, and a caller should weigh them accordingly.
	Parsed bool `json:"parsed"`
	// SyntaxReported says whether Parsed means anything for this language.
	SyntaxReported bool `json:"syntax_reported"`
}

// Locate finds every occurrence of one name in one file.
//
// The name is matched exactly against identifier text. There is no type
// resolution and nothing outside the file is consulted, so two unrelated
// declarations that happen to share a name both match. That is what makes the
// answer name-based, and it is why the tool reports its precision.
func (e *Engine) Locate(path string, content []byte, digest, wanted string) (Located, error) {
	language, known := e.LanguageOf(path)
	if !known {
		return Located{}, fail(CodeUnsupportedLanguage,
			"Occurrences are not available for "+path+"; this build understands "+
				strings.Join(e.Languages(), ", ")+".", false, nil)
	}
	g := e.grammars[language]

	parser := e.parsers.Get().(*ts.Parser)
	defer e.parsers.Put(parser)
	if err := parser.SetLanguage(g.language); err != nil {
		return Located{}, fail(CodeInternalError, "The parser could not be prepared.", false, err)
	}
	tree := e.parse(parser, content)
	if tree == nil {
		if e.Budget > 0 {
			return Located{}, fail(CodeParseIncomplete,
				"The file was not fully parsed within "+e.Budget.String()+".", false, nil)
		}
		return Located{}, fail(CodeInternalError, "The file could not be parsed.", false, nil)
	}
	defer tree.Close()

	root := tree.RootNode()
	located := Located{
		Path:           path,
		Language:       language,
		Digest:         digest,
		Occurrences:    []Occurrence{},
		SyntaxReported: g.reportsSyntax,
		Parsed:         g.reportsSyntax && !root.HasError(),
	}

	declarations, names := e.declarations(g, root, content)
	lines := lineStarts(content)

	cursor := ts.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(g.identifiers, root, content)
	for match := matches.Next(); match != nil; match = matches.Next() {
		for index := range match.Captures {
			node := match.Captures[index].Node
			if node.Utf8Text(content) != wanted {
				continue
			}
			start, end := int(node.StartByte()), int(node.EndByte())
			occurrence := Occurrence{
				Line:      int(node.StartPosition().Row) + 1,
				Column:    int(node.StartPosition().Column) + 1,
				StartByte: start,
				EndByte:   end,
				Preview:   previewAt(content, lines, int(node.StartPosition().Row)),
			}
			if declared, ok := names[byteRange{start, end}]; ok {
				occurrence.Declaration = true
				occurrence.Kind = declared.Kind
				// The container is the declaration's own container, not itself: a
				// function is not inside itself.
				occurrence.Container = declared.Container
			} else {
				occurrence.Container = innermostContaining(declarations, start, end)
			}
			located.Occurrences = append(located.Occurrences, occurrence)
			if occurrence.Declaration {
				located.Declarations++
			}
		}
	}

	sort.SliceStable(located.Occurrences, func(a, b int) bool {
		return located.Occurrences[a].StartByte < located.Occurrences[b].StartByte
	})
	return located, nil
}

type byteRange struct{ start, end int }

// declarations returns the file's nested declaration list and an index from each
// declaration's *name* extent to the declaration.
//
// The name extent is what identifies a declaration's own identifier among the
// occurrences: the declaration's full extent contains many identifiers, and only
// one of them is the name being declared.
func (e *Engine) declarations(g *grammar, root *ts.Node, content []byte) ([]Symbol, map[byteRange]Symbol) {
	cursor := ts.NewQueryCursor()
	defer cursor.Close()
	captures := g.query.CaptureNames()
	matches := cursor.Matches(g.query, root, content)

	var found []Symbol
	var nameRanges []byteRange
	for match := matches.Next(); match != nil; match = matches.Next() {
		var name, def *ts.Node
		for index := range match.Captures {
			capture := match.Captures[index]
			switch captures[capture.Index] {
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
		nameRanges = append(nameRanges, byteRange{int(name.StartByte()), int(name.EndByte())})
	}

	// nest sorts in place, so the name ranges are paired with their declarations by
	// extent afterwards rather than by position in the slice.
	byExtent := make(map[byteRange]byteRange, len(found))
	for index := range found {
		byExtent[byteRange{found[index].StartByte, found[index].EndByte}] = nameRanges[index]
	}
	nested := nest(found)

	names := make(map[byteRange]Symbol, len(nested))
	for _, symbol := range nested {
		if name, ok := byExtent[byteRange{symbol.StartByte, symbol.EndByte}]; ok {
			names[name] = symbol
		}
	}
	return nested, names
}

// innermostContaining names the tightest declaration whose extent holds a range.
func innermostContaining(declarations []Symbol, start, end int) string {
	best := ""
	bestWidth := 0
	for _, symbol := range declarations {
		if symbol.StartByte > start || symbol.EndByte < end {
			continue
		}
		width := symbol.EndByte - symbol.StartByte
		if best == "" || width < bestWidth {
			best, bestWidth = symbol.Name, width
		}
	}
	return best
}

// lineStarts records the byte offset of each line, so a preview costs a lookup
// rather than a scan of the file per occurrence.
func lineStarts(content []byte) []int {
	starts := make([]int, 1, 1+len(content)/32)
	for index, value := range content {
		if value == '\n' {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func previewAt(content []byte, starts []int, row int) string {
	if row < 0 || row >= len(starts) {
		return ""
	}
	start := starts[row]
	end := len(content)
	if row+1 < len(starts) {
		end = starts[row+1] - 1
	}
	if start > end {
		return ""
	}
	text := strings.TrimSpace(string(content[start:end]))
	if len(text) <= maxPreviewBytes {
		return text
	}
	cut := maxPreviewBytes
	for cut > 0 && !utf8Start(text[cut]) {
		cut--
	}
	return strings.TrimRight(text[:cut], " \t") + "…"
}
