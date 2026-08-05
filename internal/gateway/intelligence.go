package gateway

import (
	"context"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

type graphNameInput struct {
	Name      string `json:"name" jsonschema:"Exact declaration or call name. Required."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum results per page. Defaults to 50, maximum 200."`
	Cursor    string `json:"cursor,omitempty" jsonschema:"Continue a previous result page."`
	ProjectID string `json:"project_id,omitempty" jsonschema:"Ignored. Scope comes from the endpoint."`
	Path      string `json:"path,omitempty" jsonschema:"Ignored. The graph covers the endpoint project."`
}

type dependencyInput struct {
	From      string `json:"from" jsonschema:"Project-relative source file. Required."`
	To        string `json:"to" jsonschema:"Project-relative destination file. Required."`
	MaxDepth  int    `json:"max_depth,omitempty" jsonschema:"Maximum import edges to traverse. Defaults to 32, maximum 32."`
	ProjectID string `json:"project_id,omitempty" jsonschema:"Ignored. Scope comes from the endpoint."`
}

type impactInput struct {
	Target    string `json:"target" jsonschema:"Project-relative file or exact declaration name. Required."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum combined evidence. Defaults to 50, maximum 200."`
	ProjectID string `json:"project_id,omitempty" jsonschema:"Ignored. Scope comes from the endpoint."`
}

type repositoryMapInput struct {
	MaxChars  int    `json:"max_chars,omitempty" jsonschema:"Exact character budget. Defaults to 12000, range 256 to 100000."`
	ProjectID string `json:"project_id,omitempty" jsonschema:"Ignored. Scope comes from the endpoint."`
}

type graphProvenance struct {
	Backend         string `json:"backend"`
	SchemaVersion   int    `json:"schema_version"`
	Precision       string `json:"precision"`
	GraphGeneration uint64 `json:"graph_generation"`
	ExactGeneration uint64 `json:"exact_generation"`
	Freshness       string `json:"freshness"`
}

type graphMatchesOutput struct {
	ProjectID   string                 `json:"project_id"`
	ScopeSource string                 `json:"scope_source"`
	Root        string                 `json:"root"`
	Result      codeintel.GraphMatches `json:"result"`
	Provenance  graphProvenance        `json:"provenance"`
	Changes     *changeInfo            `json:"changes,omitempty"`
}

type dependencyOutput struct {
	ProjectID   string                       `json:"project_id"`
	ScopeSource string                       `json:"scope_source"`
	Root        string                       `json:"root"`
	Result      codeintel.DependencyResponse `json:"result"`
	Provenance  graphProvenance              `json:"provenance"`
	Changes     *changeInfo                  `json:"changes,omitempty"`
}
type impactOutput struct {
	ProjectID   string                   `json:"project_id"`
	ScopeSource string                   `json:"scope_source"`
	Root        string                   `json:"root"`
	Result      codeintel.ImpactResponse `json:"result"`
	Provenance  graphProvenance          `json:"provenance"`
	Changes     *changeInfo              `json:"changes,omitempty"`
}
type repositoryMapOutput struct {
	ProjectID   string                `json:"project_id"`
	ScopeSource string                `json:"scope_source"`
	Root        string                `json:"root"`
	Result      codeintel.MapResponse `json:"result"`
	Provenance  graphProvenance       `json:"provenance"`
	Changes     *changeInfo           `json:"changes,omitempty"`
}

type memoryGetInput struct {
	Key       string `json:"key" jsonschema:"Stable lowercase memory key. Required."`
	ProjectID string `json:"project_id,omitempty" jsonschema:"Ignored. Scope comes from the endpoint."`
}
type memorySearchInput struct {
	Query     string `json:"query,omitempty" jsonschema:"Concept or text to find. Empty lists records."`
	Kind      string `json:"kind,omitempty" jsonschema:"Optional decision, convention, fact, or note filter."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum records. Defaults to 50, maximum 200."`
	ProjectID string `json:"project_id,omitempty" jsonschema:"Ignored. Scope comes from the endpoint."`
}
type memoryPutInput struct {
	Key              string   `json:"key" jsonschema:"Stable lowercase memory key. Required."`
	Kind             string   `json:"kind" jsonschema:"decision, convention, fact, or note. Required."`
	Content          string   `json:"content" jsonschema:"Explicit project knowledge. Required."`
	Confidence       float64  `json:"confidence" jsonschema:"Confidence from 0 through 1. Required."`
	Provenance       []string `json:"provenance,omitempty" jsonschema:"Project-relative evidence paths."`
	SourceCommit     string   `json:"source_commit,omitempty" jsonschema:"Source commit; defaults to the current project commit when available."`
	ExpectedRevision *int     `json:"expected_revision,omitempty" jsonschema:"Required for update; omit only to create a new key."`
	Reviewed         bool     `json:"reviewed,omitempty" jsonschema:"Mark the written revision reviewed now."`
	ProjectID        string   `json:"project_id,omitempty" jsonschema:"Ignored. Scope comes from the endpoint."`
}
type memoryDeleteInput struct {
	Key              string `json:"key" jsonschema:"Existing memory key. Required."`
	ExpectedRevision int    `json:"expected_revision" jsonschema:"Revision returned by memory_get. Required."`
	ProjectID        string `json:"project_id,omitempty" jsonschema:"Ignored. Scope comes from the endpoint."`
}

type memoryRecordOutput struct {
	ProjectID     string                 `json:"project_id"`
	ScopeSource   string                 `json:"scope_source"`
	Root          string                 `json:"root"`
	Record        codeintel.MemoryRecord `json:"record"`
	CurrentCommit string                 `json:"current_commit,omitempty"`
	StaleSource   bool                   `json:"stale_source"`
}
type memorySearchOutput struct {
	ProjectID     string                         `json:"project_id"`
	ScopeSource   string                         `json:"scope_source"`
	Root          string                         `json:"root"`
	Result        codeintel.MemorySearchResponse `json:"result"`
	CurrentCommit string                         `json:"current_commit,omitempty"`
	StaleKeys     []string                       `json:"stale_keys"`
}
type memoryDeleteOutput struct {
	ProjectID   string `json:"project_id"`
	ScopeSource string `json:"scope_source"`
	Root        string `json:"root"`
	Key         string `json:"key"`
	Deleted     bool   `json:"deleted"`
}

// registerIntelligenceTools exposes graph and explicit memory without exposing
// SQLite, internal identifiers, or any client-supplied project scope.
func (g *Gateway) registerIntelligenceTools(server *mcp.Server, resolved serveContext) {
	mcp.AddTool(server, &mcp.Tool{Name: "callers_find", Description: "Find saved call sites whose callee identifier matches a name. Precision is name_match; ambiguity and freshness are explicit."}, func(ctx context.Context, _ *mcp.CallToolRequest, input graphNameInput) (*mcp.CallToolResult, graphMatchesOutput, error) {
		g.flushProject(ctx, resolved)
		result, err := g.searchClient(resolved).Callers(ctx, codeintel.GraphRequest{Name: input.Name, Limit: input.Limit, Cursor: input.Cursor})
		if err != nil {
			return nil, graphMatchesOutput{}, asSearchToolError(err)
		}
		provenance, changes := g.graphProof(ctx, resolved, result.Generation)
		return nil, graphMatchesOutput{resolved.project.ID, "route_and_registry", projectstack.WorkspaceMount, result, provenance, changes}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "callees_find", Description: "Find saved calls made inside declarations with an exact name. Precision is name_match; ambiguity and freshness are explicit."}, func(ctx context.Context, _ *mcp.CallToolRequest, input graphNameInput) (*mcp.CallToolResult, graphMatchesOutput, error) {
		g.flushProject(ctx, resolved)
		result, err := g.searchClient(resolved).Callees(ctx, codeintel.GraphRequest{Name: input.Name, Limit: input.Limit, Cursor: input.Cursor})
		if err != nil {
			return nil, graphMatchesOutput{}, asSearchToolError(err)
		}
		provenance, changes := g.graphProof(ctx, resolved, result.Generation)
		return nil, graphMatchesOutput{resolved.project.ID, "route_and_registry", projectstack.WorkspaceMount, result, provenance, changes}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dependency_path", Description: "Find a deterministic shortest path through saved import syntax between two project files. No package-manager resolution or LSP is implied."}, func(ctx context.Context, _ *mcp.CallToolRequest, input dependencyInput) (*mcp.CallToolResult, dependencyOutput, error) {
		g.flushProject(ctx, resolved)
		result, err := g.searchClient(resolved).DependencyPath(ctx, codeintel.DependencyRequest{From: input.From, To: input.To, MaxDepth: input.MaxDepth})
		if err != nil {
			return nil, dependencyOutput{}, asSearchToolError(err)
		}
		provenance, changes := g.graphProof(ctx, resolved, result.Generation)
		return nil, dependencyOutput{resolved.project.ID, "route_and_registry", projectstack.WorkspaceMount, result, provenance, changes}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "impact_analyze", Description: "Find direct reverse-import and name-matched caller evidence for a file or declaration. It does not claim type-resolved transitive impact."}, func(ctx context.Context, _ *mcp.CallToolRequest, input impactInput) (*mcp.CallToolResult, impactOutput, error) {
		g.flushProject(ctx, resolved)
		result, err := g.searchClient(resolved).Impact(ctx, codeintel.ImpactRequest{Target: input.Target, Limit: input.Limit})
		if err != nil {
			return nil, impactOutput{}, asSearchToolError(err)
		}
		provenance, changes := g.graphProof(ctx, resolved, result.Generation)
		return nil, impactOutput{resolved.project.ID, "route_and_registry", projectstack.WorkspaceMount, result, provenance, changes}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "repository_map", Description: "Return a deterministic importance-ranked project map inside an exact character budget, with explicit truncation and graph freshness."}, func(ctx context.Context, _ *mcp.CallToolRequest, input repositoryMapInput) (*mcp.CallToolResult, repositoryMapOutput, error) {
		g.flushProject(ctx, resolved)
		result, err := g.searchClient(resolved).RepositoryMap(ctx, codeintel.MapRequest{MaxChars: input.MaxChars})
		if err != nil {
			return nil, repositoryMapOutput{}, asSearchToolError(err)
		}
		provenance, changes := g.graphProof(ctx, resolved, result.Generation)
		return nil, repositoryMapOutput{resolved.project.ID, "route_and_registry", projectstack.WorkspaceMount, result, provenance, changes}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "memory_get", Description: "Read one explicit reviewed project-memory record by stable key. No repository content is written into memory automatically."}, func(ctx context.Context, _ *mcp.CallToolRequest, input memoryGetInput) (*mcp.CallToolResult, memoryRecordOutput, error) {
		record, err := g.searchClient(resolved).MemoryGet(ctx, codeintel.MemoryGetRequest{Key: input.Key})
		if err != nil {
			return nil, memoryRecordOutput{}, asSearchToolError(err)
		}
		return nil, g.memoryRecordProof(ctx, resolved, record), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "memory_search", Description: "Search explicit project memory semantically and lexically; an empty query lists records. Stale commit, review, and confidence labels remain visible."}, func(ctx context.Context, _ *mcp.CallToolRequest, input memorySearchInput) (*mcp.CallToolResult, memorySearchOutput, error) {
		result, err := g.searchClient(resolved).MemorySearch(ctx, codeintel.MemorySearchRequest{Query: input.Query, Kind: input.Kind, Limit: input.Limit})
		if err != nil {
			return nil, memorySearchOutput{}, asSearchToolError(err)
		}
		current := g.currentCommit(ctx, resolved)
		stale := []string{}
		for _, match := range result.Matches {
			if current != "" && match.Record.SourceCommit != "" && match.Record.SourceCommit != current {
				stale = append(stale, match.Record.Key)
			}
		}
		sort.Strings(stale)
		return nil, memorySearchOutput{resolved.project.ID, "route_and_registry", projectstack.WorkspaceMount, result, current, stale}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "memory_put", Description: "Explicitly create or optimistic-revision update project memory. Existing keys cannot be overwritten without the revision returned by memory_get."}, func(ctx context.Context, _ *mcp.CallToolRequest, input memoryPutInput) (*mcp.CallToolResult, memoryRecordOutput, error) {
		commit := input.SourceCommit
		if commit == "" {
			commit = g.currentCommit(ctx, resolved)
		}
		record, err := g.searchClient(resolved).MemoryPut(ctx, codeintel.MemoryPutRequest{Key: input.Key, Kind: input.Kind, Content: input.Content, Confidence: input.Confidence, Provenance: input.Provenance, SourceCommit: commit, ExpectedRevision: input.ExpectedRevision, Reviewed: input.Reviewed})
		if err != nil {
			return nil, memoryRecordOutput{}, asSearchToolError(err)
		}
		return nil, g.memoryRecordProof(ctx, resolved, record), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "memory_delete", Description: "Explicitly delete one project-memory key at the revision the client read. Concurrent changes are refused."}, func(ctx context.Context, _ *mcp.CallToolRequest, input memoryDeleteInput) (*mcp.CallToolResult, memoryDeleteOutput, error) {
		if err := g.searchClient(resolved).MemoryDelete(ctx, codeintel.MemoryDeleteRequest{Key: input.Key, ExpectedRevision: input.ExpectedRevision}); err != nil {
			return nil, memoryDeleteOutput{}, asSearchToolError(err)
		}
		return nil, memoryDeleteOutput{resolved.project.ID, "route_and_registry", projectstack.WorkspaceMount, input.Key, true}, nil
	})
}

func (g *Gateway) flushProject(ctx context.Context, resolved serveContext) {
	if g.options.Flush == nil {
		return
	}
	flushCtx, cancel := context.WithTimeout(ctx, searchFlushBudget)
	defer cancel()
	g.options.Flush(flushCtx, resolved.project.ID)
}

func (g *Gateway) graphProof(ctx context.Context, resolved serveContext, generation uint64) (graphProvenance, *changeInfo) {
	proof := graphProvenance{Backend: codeintel.GraphBackend, SchemaVersion: codeintel.IntelligenceSchemaVersion, Precision: "name_match", GraphGeneration: generation, Freshness: freshnessUnknown}
	if status, err := g.searchClient(resolved).Status(ctx); err == nil {
		proof.ExactGeneration = status.Generation
		if status.Graph != nil {
			proof.Freshness = status.Graph.Freshness
		}
	}
	var changes *changeInfo
	if g.options.Changes != nil {
		state, watching := g.options.Changes(resolved.project.ID)
		var freshness string
		changes, freshness = describeChanges(state, watching, false)
		if freshness != freshnessFresh {
			proof.Freshness = freshness
		} else if proof.ExactGeneration == generation {
			proof.Freshness = freshnessFresh
		}
	}
	if proof.GraphGeneration != proof.ExactGeneration && proof.ExactGeneration != 0 {
		proof.Freshness = freshnessStale
	}
	if proof.Freshness == freshnessFresh {
		changes = nil
	}
	return proof, changes
}

func (g *Gateway) currentCommit(ctx context.Context, resolved serveContext) string {
	source := g.sourceOf(ctx, resolved)
	if source == nil {
		return ""
	}
	return source.Commit
}

func (g *Gateway) memoryRecordProof(ctx context.Context, resolved serveContext, record codeintel.MemoryRecord) memoryRecordOutput {
	current := g.currentCommit(ctx, resolved)
	return memoryRecordOutput{ProjectID: resolved.project.ID, ScopeSource: "route_and_registry", Root: projectstack.WorkspaceMount, Record: record, CurrentCommit: current, StaleSource: current != "" && record.SourceCommit != "" && record.SourceCommit != current}
}
