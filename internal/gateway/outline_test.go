package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

func callFileOutline(t *testing.T, session *mcp.ClientSession, arguments map[string]any) (fileOutlineOutput, *mcp.CallToolResult) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "file_outline",
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("file_outline transport failure: %v", err)
	}
	if result.IsError {
		return fileOutlineOutput{}, result
	}

	var output fileOutlineOutput
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("file_outline output is not the expected shape: %v", err)
	}
	return output, result
}

func TestFileOutlineAnswersWithDeclarationsAndSaysHowPreciseTheyAre(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output, _ := callFileOutline(t, session, map[string]any{"path": "internal/a.go"})

	if output.ProjectID != "alpha-aaaaaaaa" || output.ScopeSource != "route_and_registry" {
		t.Errorf("scope = %q from %q", output.ProjectID, output.ScopeSource)
	}
	if output.Root != projectstack.WorkspaceMount {
		t.Errorf("root = %q, want the in-container workspace", output.Root)
	}
	if len(output.Symbols) != 1 {
		t.Fatalf("symbols = %+v", output.Symbols)
	}
	symbol := output.Symbols[0]
	if symbol.Name != "Needle" || symbol.Kind != "function" {
		t.Errorf("symbol = %+v", symbol)
	}
	if symbol.EndByte <= symbol.StartByte {
		t.Errorf("the symbol carries no extent: %+v", symbol)
	}
	// The whole point of stating precision is that a caller must not read a
	// name-matched, syntax-only answer as a type-resolved one.
	if output.Provenance.Precision != "syntax" {
		t.Errorf("precision = %q, want syntax", output.Provenance.Precision)
	}
	if output.Provenance.Backend != symbolBackend || output.Provenance.SchemaVersion != 1 {
		t.Errorf("provenance = %+v", output.Provenance)
	}
	if output.Provenance.ReadAt == "" {
		t.Error("the answer does not say when the file was read")
	}
	// A digest rather than an index generation: the outline read the file, so there
	// is no generation it could be behind.
	if output.Digest != "abc123" {
		t.Errorf("digest = %q", output.Digest)
	}
	if !output.Syntax.Reported || !output.Syntax.Valid {
		t.Errorf("syntax = %+v", output.Syntax)
	}
}

func TestFileOutlineSendsOnlyThePathAndNothingThatCouldRedirectScope(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output, _ := callFileOutline(t, session, map[string]any{
		"path":            "internal/a.go",
		"project_id":      "beta-bbbbbbbb",
		"repository_root": "/somewhere/else",
	})

	if output.ProjectID != "alpha-aaaaaaaa" {
		t.Errorf("a supplied project_id changed the answered project: %q", output.ProjectID)
	}
	if len(service.outlineRequests) != 1 || service.outlineRequests[0] != "internal/a.go" {
		t.Errorf("the service was asked about %v", service.outlineRequests)
	}
}

// A project whose container predates the symbol layer reports no languages. The
// tool must not be advertised then: a caller that trusts the capability list would
// otherwise learn the truth from a refusal.
func TestFileOutlineIsAdvertisedOnlyWhenTheServiceCanActuallyOutline(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	info, _ := callTool[projectInfoOutput](t, session, "project_info", nil)
	if !advertises(info.Capabilities, "file_outline") {
		t.Errorf("capabilities = %v, want file_outline", info.Capabilities)
	}
	if len(info.OutlineLanguages) != 1 || info.OutlineLanguages[0] != "go" {
		t.Errorf("outline_languages = %v", info.OutlineLanguages)
	}

	service.outlineLanguages = nil
	session = f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	info, _ = callTool[projectInfoOutput](t, session, "project_info", nil)
	if advertises(info.Capabilities, "file_outline") {
		t.Errorf("capabilities = %v; a service that outlines nothing must not advertise the tool", info.Capabilities)
	}
	if len(info.OutlineLanguages) != 0 {
		t.Errorf("outline_languages = %v, want none", info.OutlineLanguages)
	}
}

func TestAFileOutlineRefusalCarriesWhatToDoInstead(t *testing.T) {
	cases := []struct {
		name    string
		code    string
		message string
		want    []string
	}{
		{
			// An empty outline would read as "this file declares nothing", so the
			// refusal has to be explicit and has to name a tool that does work.
			name:    "an unsupported language",
			code:    codeintel.CodeLanguageUnsupported,
			message: "Outlines are not available for \".md\"; this build understands go.",
			want:    []string{"LANGUAGE_UNSUPPORTED", "exact_search", "project_info"},
		},
		{
			// A file plainly present in the checkout can be absent here, so the
			// advice names the reason rather than inviting the same request again.
			name:    "a path the project does not hold",
			code:    codeintel.CodeFileNotFound,
			message: "There is no such file in this project: internal/gone.go",
			want:    []string{"FILE_NOT_FOUND", "ignore rules"},
		},
		{
			name:    "an escaping path",
			code:    codeintel.CodeInvalidPath,
			message: "the path must stay inside the project: \"../outside.go\"",
			want:    []string{"INVALID_PATH", "Correct the request"},
		},
		{
			name:    "a file too costly to parse",
			code:    codeintel.CodeParseIncomplete,
			message: "The file was not fully parsed within 5s.",
			want:    []string{"PARSE_INCOMPLETE", "search within it"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true, "alpha-aaaaaaaa")
			service := newFakeService(t)
			service.outlineStatus = http.StatusBadRequest
			service.outlineResponse = `{"error":{"code":"` + c.code +
				`","message":` + quote(c.message) + `,"retryable":false}}`
			f.service["alpha-aaaaaaaa"] = service.address()

			session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
			_, result := callFileOutline(t, session, map[string]any{"path": "x"})
			if result == nil || !result.IsError {
				t.Fatal("the refusal did not reach the caller as an error")
			}
			text := resultText(result)
			for _, want := range c.want {
				if !strings.Contains(text, want) {
					t.Errorf("the refusal reads %q, which does not mention %q", text, want)
				}
			}
		})
	}
}

func quote(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}

// advertises reports whether a capability list names a tool.
func advertises(capabilities []string, tool string) bool {
	for _, capability := range capabilities {
		if capability == tool {
			return true
		}
	}
	return false
}
