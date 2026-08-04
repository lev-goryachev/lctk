package gateway

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

// symbolBackend names the engine behind the symbol answers, reported as
// provenance rather than used for dispatch.
const symbolBackend = "tree-sitter"

// precisionSyntax is what these answers are.
//
// It means the declarations and occurrences are what the syntax says, matched by
// name, with nothing type-resolved and nothing outside a file consulted. Two
// unrelated declarations that share a name both match, and a caller that reads
// this as a type-resolved answer will act wrongly on it — which is why the value
// is in the answer and not only in the documentation.
const precisionSyntax = "syntax"

// precisionNameMatch is what a cross-file answer is.
//
// It is weaker than precisionSyntax and deliberately named differently: within one
// file the syntax settles what is a declaration, while across files nothing here
// resolves which declaration a use refers to.
const precisionNameMatch = "name_match"

// findDefinitionInput is the public request schema.
type findDefinitionInput struct {
	Name string `json:"name" jsonschema:"The identifier to find, exactly as written. Required. Matching is by name, not by type."`

	ProjectID string `json:"project_id,omitempty" jsonschema:"Ignored. The authoritative project comes from the endpoint."`
}

// findReferencesInput is the public request schema.
type findReferencesInput struct {
	Name string `json:"name" jsonschema:"The identifier to find, exactly as written. Required. Matching is by name, not by type."`
	// MaxFiles is exposed because the work is proportional to it: every candidate
	// file is parsed. A caller asking about a very common name should know it can
	// bound the answer rather than receive a truncated one it did not choose.
	MaxFiles int `json:"max_files,omitempty" jsonschema:"How many files to examine at most. Defaults to 200, which is also the maximum."`

	ProjectID string `json:"project_id,omitempty" jsonschema:"Ignored. The authoritative project comes from the endpoint."`
}

// symbolLocation is one place a name appears.
type symbolLocation struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	// StartByte and EndByte bound the identifier itself.
	StartByte int `json:"start_byte"`
	EndByte   int `json:"end_byte"`
	// Declaration says this is where the name is declared rather than used.
	Declaration bool `json:"declaration"`
	// Kind is the declaration's kind, set only when Declaration is true.
	Kind string `json:"kind,omitempty"`
	// Container is the enclosing declaration, so a location can be placed without
	// opening the file.
	Container string `json:"container,omitempty"`
	Preview   string `json:"preview,omitempty"`
	// FileParses reports whether the file is syntactically whole, absent for a
	// language where that verdict is not published. A reference inside a file that
	// does not parse is worth less, and saying so costs nothing.
	FileParses *bool `json:"file_parses,omitempty"`
}

// symbolLookupOutput is the public response schema for both tools.
type symbolLookupOutput struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	// Locations are ordered by path and position.
	Locations []symbolLocation `json:"locations"`
	Total     int              `json:"total"`
	// Declarations counts the locations that declare the name, so a caller can tell
	// one definition from twelve without walking the list.
	Declarations int `json:"declarations"`
	// FilesConsidered is how many files were examined, and FilesWithMatches how many
	// held an actual identifier. The gap is what this answers over a text search.
	FilesConsidered  int `json:"files_considered"`
	FilesWithMatches int `json:"files_with_matches"`
	// Truncated says the answer is bounded, so "nothing else refers to this" is not
	// something a caller may conclude from it.
	Truncated bool `json:"truncated,omitempty"`
	// Skipped names what was not examined and why, rather than leaving a partial
	// answer to look complete.
	SkippedUnsupportedLanguage int `json:"skipped_unsupported_language,omitempty"`
	SkippedUnreadable          int `json:"skipped_unreadable,omitempty"`

	ScopeSource string           `json:"scope_source"`
	Root        string           `json:"root"`
	Provenance  symbolProvenance `json:"provenance"`
	// Changes says what the host has observed but not yet applied. A lookup chooses
	// its candidate files from the index, so an index that is behind means files
	// this answer never opened.
	Changes *changeInfo `json:"changes,omitempty"`
}

// symbolProvenance says what produced an answer and how precise it is.
type symbolProvenance struct {
	Backend       string `json:"backend"`
	SchemaVersion int    `json:"schema_version"`
	// Precision is name_match for a cross-file answer: within a file the syntax
	// settles what is a declaration, and across files nothing here resolves which
	// declaration a use refers to.
	Precision string `json:"precision"`
	// IndexGeneration is the generation that chose the candidate files, and
	// Freshness the host's verdict on it. Both are here because this answer, unlike
	// an outline, is only as complete as the index that narrowed it.
	IndexGeneration uint64 `json:"index_generation"`
	IndexedAt       string `json:"indexed_at,omitempty"`
	Freshness       string `json:"freshness,omitempty"`
}

// registerSymbolTools adds find_definition and find_references.
func (g *Gateway) registerSymbolTools(server *mcp.Server, resolved serveContext) {
	if resolved.status.ServiceAddress == "" {
		return
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "find_definition",
		Description: "Find where an identifier is declared in this project. " +
			"Matching is by name: two unrelated declarations that share a name both appear, " +
			"and nothing here resolves which one a particular use refers to. " +
			"An occurrence inside a comment or a string is never reported, because the answer " +
			"comes from a syntax tree rather than from a text search. " +
			"The scope comes from the endpoint; an argument naming a project is ignored.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input findDefinitionInput) (*mcp.CallToolResult, symbolLookupOutput, error) {
		return g.lookupSymbol(ctx, resolved, input.Name, true, 0)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "find_references",
		Description: "Find every place an identifier is used in this project, and where it is declared. " +
			"Each location says whether it is a declaration and which declaration encloses it. " +
			"Matching is by name and nothing is type-resolved, so a same-named identifier from " +
			"elsewhere appears too. " +
			"An occurrence inside a comment or a string is never reported. " +
			"The answer is bounded: when it says it was truncated, absence of a reference proves nothing. " +
			"The scope comes from the endpoint; an argument naming a project is ignored.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input findReferencesInput) (*mcp.CallToolResult, symbolLookupOutput, error) {
		return g.lookupSymbol(ctx, resolved, input.Name, false, input.MaxFiles)
	})
}

// lookupSymbol runs one identifier lookup and flattens it for a caller.
func (g *Gateway) lookupSymbol(ctx context.Context, resolved serveContext,
	name string, declarationsOnly bool, maxFiles int,
) (*mcp.CallToolResult, symbolLookupOutput, error) {
	// The index chooses which files are parsed, so the same flush a search performs
	// applies here for the same reason: a file the index has not caught up with is a
	// file this lookup never opens.
	if g.options.Flush != nil {
		flushCtx, cancel := context.WithTimeout(ctx, searchFlushBudget)
		g.options.Flush(flushCtx, resolved.project.ID)
		cancel()
	}

	located, err := g.searchClient(resolved).Locate(ctx, name, declarationsOnly, maxFiles)
	if err != nil {
		return nil, symbolLookupOutput{}, asSearchToolError(err)
	}

	output := symbolLookupOutput{
		ProjectID:                  resolved.project.ID,
		Name:                       located.Name,
		Locations:                  []symbolLocation{},
		Declarations:               located.Declarations,
		FilesConsidered:            located.FilesConsidered,
		FilesWithMatches:           len(located.Files),
		Truncated:                  located.FilesTruncated,
		SkippedUnsupportedLanguage: located.SkippedUnsupported,
		SkippedUnreadable:          located.SkippedUnreadable,
		ScopeSource:                "route_and_registry",
		Root:                       projectstack.WorkspaceMount,
		Provenance: symbolProvenance{
			Backend:         symbolBackend,
			SchemaVersion:   codeintel.SchemaVersion,
			Precision:       precisionNameMatch,
			IndexGeneration: located.Generation,
			IndexedAt:       located.IndexedAt,
		},
	}
	for _, file := range located.Files {
		parses := file.Parsed
		for _, occurrence := range file.Occurrences {
			location := symbolLocation{
				Path:        file.Path,
				Line:        occurrence.Line,
				Column:      occurrence.Column,
				StartByte:   occurrence.StartByte,
				EndByte:     occurrence.EndByte,
				Declaration: occurrence.Declaration,
				Kind:        occurrence.Kind,
				Container:   occurrence.Container,
				Preview:     occurrence.Preview,
			}
			if file.SyntaxReported {
				location.FileParses = &parses
			}
			output.Locations = append(output.Locations, location)
		}
	}
	output.Total = len(output.Locations)

	if g.options.Changes != nil {
		state, watching := g.options.Changes(resolved.project.ID)
		changes, freshness := describeChanges(state, watching, false)
		output.Provenance.Freshness = freshness
		if freshness != freshnessFresh {
			output.Changes = changes
		}
	}
	return nil, output, nil
}
