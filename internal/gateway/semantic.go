package gateway

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

// semanticSearchInput is the stable MCP request for conceptual code retrieval.
// Scope-like arguments are ignored so an over-specified model request cannot
// weaken route-and-registry authority.
type semanticSearchInput struct {
	Query string `json:"query" jsonschema:"The concept, behavior, or code intent to find. Required."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum hybrid chunk matches. Defaults to 20, maximum 100."`

	ProjectID      string `json:"project_id,omitempty" jsonschema:"Ignored. The authoritative project comes from the endpoint."`
	RepositoryRoot string `json:"repository_root,omitempty" jsonschema:"Ignored. The authoritative root comes from the registry."`
	Path           string `json:"path,omitempty" jsonschema:"Ignored. Semantic search covers the endpoint's project."`
}

// semanticProvenance exposes every compatibility and freshness dimension an
// agent needs before trusting a conceptual result.
type semanticProvenance struct {
	Backend            string `json:"backend"`
	SchemaVersion      int    `json:"schema_version"`
	Model              string `json:"model"`
	Dimensions         int    `json:"dimensions"`
	SemanticGeneration uint64 `json:"semantic_generation"`
	ExactGeneration    uint64 `json:"exact_generation"`
	Freshness          string `json:"freshness"`
}

// semanticSearchOutput keeps route scope beside the ranked answer just as
// exact_search does.
type semanticSearchOutput struct {
	ProjectID   string                    `json:"project_id"`
	Matches     []codeintel.SemanticMatch `json:"matches"`
	Total       int                       `json:"total"`
	Truncated   bool                      `json:"truncated"`
	ScopeSource string                    `json:"scope_source"`
	Root        string                    `json:"root"`
	Provenance  semanticProvenance        `json:"provenance"`
	Changes     *changeInfo               `json:"changes,omitempty"`
}

// registerSemanticTool adds one project-bound action. The tool remains visible
// when inference is temporarily unavailable so the caller receives the typed
// remediation instead of interpreting a disappearing capability.
func (g *Gateway) registerSemanticTool(server *mcp.Server, resolved serveContext) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "code_search_semantic",
		Description: "Find code and project text by concept in this project's saved working tree. " +
			"The result fuses local embedding similarity with lexical evidence, reports both ranks, " +
			"and states semantic versus exact index freshness. Scope comes only from the endpoint.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input semanticSearchInput) (*mcp.CallToolResult, semanticSearchOutput, error) {
		if resolved.status.ServiceAddress == "" {
			return nil, semanticSearchOutput{}, &searchToolError{
				code:    codeintel.CodeSearchUnsupported,
				message: "This project does not expose a code-intelligence service.",
				action:  "Restart it with lctk project restart so it is recreated with the current stack definition.",
			}
		}
		if g.options.Flush != nil {
			flushCtx, cancel := context.WithTimeout(ctx, searchFlushBudget)
			g.options.Flush(flushCtx, resolved.project.ID)
			cancel()
		}
		response, err := g.searchClient(resolved).SemanticSearch(ctx, codeintel.SemanticRequest{
			Query: input.Query, Limit: input.Limit,
		})
		if err != nil {
			return nil, semanticSearchOutput{}, asSearchToolError(err)
		}
		output := semanticSearchOutput{
			ProjectID: resolved.project.ID, Matches: response.Matches, Total: response.Total,
			Truncated: response.Truncated, ScopeSource: "route_and_registry", Root: projectstack.WorkspaceMount,
			Provenance: semanticProvenance{
				Backend: codeintel.SemanticBackend, SchemaVersion: codeintel.SchemaVersion,
				Model: response.Model, Dimensions: response.Dimensions,
				SemanticGeneration: response.Generation, ExactGeneration: response.ExactGeneration,
				Freshness: response.Freshness,
			},
		}
		if g.options.Changes != nil {
			state, watching := g.options.Changes(resolved.project.ID)
			changes, diskFreshness := describeChanges(state, watching, false)
			if diskFreshness != freshnessFresh {
				output.Provenance.Freshness = freshnessStale
				output.Changes = changes
			}
		}
		return nil, output, nil
	})
}
