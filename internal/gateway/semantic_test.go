package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

func callSemanticSearch(t *testing.T, session *mcp.ClientSession, arguments map[string]any) (semanticSearchOutput, *mcp.CallToolResult) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "code_search_semantic", Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("code_search_semantic transport failure: %v", err)
	}
	if result.IsError {
		return semanticSearchOutput{}, result
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output semanticSearchOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("code_search_semantic output is not the expected shape: %v", err)
	}
	return output, result
}

func TestSemanticSearchIsProjectScopedAndCarriesHybridProvenance(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa", "beta-bbbbbbbb")
	service := newFakeService(t)
	service.semanticReady = true
	f.service["alpha-aaaaaaaa"] = service.address()
	f.changes["alpha-aaaaaaaa"] = ChangeState{Watching: true}

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output, result := callSemanticSearch(t, session, map[string]any{
		"query": "retry a failed request with backoff", "limit": 7,
		"project_id": "beta-bbbbbbbb", "repository_root": "C:\\other", "path": "../other",
	})
	if result.IsError {
		t.Fatalf("semantic search failed: %s", resultText(result))
	}
	if output.ProjectID != "alpha-aaaaaaaa" || output.ScopeSource != "route_and_registry" {
		t.Fatalf("scope = project %q via %q", output.ProjectID, output.ScopeSource)
	}
	if output.Root != projectstack.WorkspaceMount {
		t.Fatalf("root = %q, want %q", output.Root, projectstack.WorkspaceMount)
	}
	if len(service.semanticRequests) != 1 || service.semanticRequests[0]["query"] != "retry a failed request with backoff" {
		t.Fatalf("service requests = %+v", service.semanticRequests)
	}
	for _, forbidden := range []string{"project_id", "repository_root", "path"} {
		if _, crossed := service.semanticRequests[0][forbidden]; crossed {
			t.Fatalf("ignored scope field %q crossed the adapter boundary", forbidden)
		}
	}
	if len(output.Matches) != 1 || output.Matches[0].Path != "internal/retry.go" {
		t.Fatalf("matches = %+v", output.Matches)
	}
	match := output.Matches[0]
	if match.VectorRank != 1 || match.LexicalRank != 1 || match.ChunkPrecision != "syntax" {
		t.Fatalf("hybrid evidence = %+v", match)
	}
	if output.Provenance.Backend != codeintel.SemanticBackend || output.Provenance.Freshness != "fresh" ||
		output.Provenance.SemanticGeneration != output.Provenance.ExactGeneration {
		t.Fatalf("provenance = %+v", output.Provenance)
	}
	info, _ := callTool[projectInfoOutput](t, session, "project_info", nil)
	if !containsCapability(info.Capabilities, "code_search_semantic") {
		t.Fatalf("capabilities = %v, want code_search_semantic", info.Capabilities)
	}
}

func TestSemanticFailureKeepsTypedRemediation(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	service.semanticStatus = 503
	service.semanticResponse = `{"error":{"code":"EMBEDDING_UNAVAILABLE",` +
		`"message":"The local embedding service could not be reached.","retryable":true}}`
	f.service["alpha-aaaaaaaa"] = service.address()

	_, result := callSemanticSearch(t, f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"]),
		map[string]any{"query": "retry"})
	message := resultText(result)
	if !result.IsError || !strings.Contains(message, codeintel.CodeEmbeddingUnavailable) ||
		!strings.Contains(message, "lctk doctor") {
		t.Fatalf("typed error = %q", message)
	}
}

func containsCapability(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
