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

func callSymbolTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) (symbolLookupOutput, *mcp.CallToolResult) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s transport failure: %v", name, err)
	}
	if result.IsError {
		return symbolLookupOutput{}, result
	}
	var output symbolLookupOutput
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("%s output is not the expected shape: %v", name, err)
	}
	return output, result
}

func TestFindReferencesFlattensLocationsAndSaysHowPreciseTheyAre(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output, _ := callSymbolTool(t, session, "find_references", map[string]any{"name": "Needle"})

	if output.ProjectID != "alpha-aaaaaaaa" || output.ScopeSource != "route_and_registry" {
		t.Errorf("scope = %q from %q", output.ProjectID, output.ScopeSource)
	}
	if output.Root != projectstack.WorkspaceMount {
		t.Errorf("root = %q", output.Root)
	}
	if output.Total != 2 || len(output.Locations) != 2 {
		t.Fatalf("locations = %+v", output.Locations)
	}
	// A cross-file answer is name-matched and must not be presented as anything
	// stronger. This is the field a caller reads to decide how much to trust it.
	if output.Provenance.Precision != precisionNameMatch {
		t.Errorf("precision = %q, want %q", output.Provenance.Precision, precisionNameMatch)
	}
	// The lookup narrows through the index, so the generation belongs in the answer
	// exactly as it does for a search.
	if output.Provenance.IndexGeneration != 4 {
		t.Errorf("index_generation = %d, want 4", output.Provenance.IndexGeneration)
	}
	if output.Provenance.Backend != symbolBackend {
		t.Errorf("backend = %q", output.Provenance.Backend)
	}

	declaration := output.Locations[0]
	if !declaration.Declaration || declaration.Kind != "function" {
		t.Errorf("the first location is not the declaration: %+v", declaration)
	}
	use := output.Locations[1]
	if use.Declaration || use.Container != "Other" {
		t.Errorf("the second location is not a use inside Other: %+v", use)
	}
	// The file's syntax verdict travels with each location, because a reference in a
	// file that does not parse is worth less than one in a file that does.
	if use.FileParses == nil || !*use.FileParses {
		t.Errorf("the location does not carry the file's syntax verdict: %+v", use)
	}
	if output.Declarations != 1 {
		t.Errorf("declarations = %d, want 1", output.Declarations)
	}
	// The gap between these two is what a syntax-aware lookup answers over a text
	// search: a file can hold the letters and no identifier.
	if output.FilesConsidered != 3 || output.FilesWithMatches != 1 {
		t.Errorf("considered %d, matched %d", output.FilesConsidered, output.FilesWithMatches)
	}
	if output.SkippedUnsupportedLanguage != 1 {
		t.Errorf("skipped_unsupported_language = %d, want 1", output.SkippedUnsupportedLanguage)
	}
}

func TestFindDefinitionAsksOnlyForDeclarations(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	callSymbolTool(t, session, "find_definition", map[string]any{"name": "Needle"})

	if len(service.locateRequests) != 1 {
		t.Fatalf("requests = %d", len(service.locateRequests))
	}
	sent := service.locateRequests[0]
	if sent["name"] != "Needle" {
		t.Errorf("name = %v", sent["name"])
	}
	// The distinction between the two tools is one flag on the wire, and it has to
	// actually be set: otherwise find_definition is find_references with a different
	// description.
	if sent["declarations_only"] != true {
		t.Errorf("declarations_only = %v, want true: %+v", sent["declarations_only"], sent)
	}
}

func TestASymbolLookupSendsNothingThatCouldRedirectScope(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa", "beta-bbbbbbbb")
	alpha := newFakeService(t)
	beta := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = alpha.address()
	f.service["beta-bbbbbbbb"] = beta.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output, _ := callSymbolTool(t, session, "find_references", map[string]any{
		"name":       "Needle",
		"project_id": "beta-bbbbbbbb",
	})

	if output.ProjectID != "alpha-aaaaaaaa" {
		t.Errorf("a supplied project_id changed the answered project: %q", output.ProjectID)
	}
	if len(beta.locateRequests) != 0 {
		t.Errorf("the other project's service was contacted: %+v", beta.locateRequests)
	}
	for _, forbidden := range []string{"project_id", "repository_root", "path"} {
		if _, present := alpha.locateRequests[0][forbidden]; present {
			t.Errorf("%q reached the backend: %+v", forbidden, alpha.locateRequests[0])
		}
	}
}

func TestAMaxFilesBoundReachesTheService(t *testing.T) {
	// The work is proportional to this, so a caller that sets it must actually get
	// it rather than a silently unbounded lookup.
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	callSymbolTool(t, session, "find_references", map[string]any{"name": "Needle", "max_files": 5})

	if got := service.locateRequests[0]["max_files"]; got != float64(5) {
		t.Errorf("max_files = %v, want 5", got)
	}
}

func TestATruncatedLookupSaysSoRatherThanImplyingCompleteness(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	service.locateResponse = `{"name":"err","generation":4,"files":[],` +
		`"occurrences":0,"declarations":0,"files_considered":200,"files_truncated":true}`
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output, _ := callSymbolTool(t, session, "find_references", map[string]any{"name": "err"})

	if !output.Truncated {
		t.Error("a truncated lookup did not say so, so an empty tail reads as an absence of references")
	}
	if output.Locations == nil {
		t.Error("locations is nil rather than empty")
	}
}

func TestASymbolLookupRefusalCarriesWhatToDo(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	service.locateStatus = http.StatusBadRequest
	service.locateResponse = `{"error":{"code":"` + codeintel.CodeInvalidPattern +
		`","message":"\"Needle|Other\" is not an identifier; a name may hold letters, digits, underscores, and dollars","retryable":false}}`
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	_, result := callSymbolTool(t, session, "find_references", map[string]any{"name": "Needle|Other"})
	if result == nil || !result.IsError {
		t.Fatal("the refusal did not reach the caller as an error")
	}
	text := resultText(result)
	for _, want := range []string{"INVALID_PATTERN", "is not an identifier", "Correct the request"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal reads %q, which does not mention %q", text, want)
		}
	}
}

func TestTheSymbolToolsAreAdvertisedWithTheOutlineOne(t *testing.T) {
	// All three come from the same engine, so a service that cannot outline cannot
	// answer a lookup either, and advertising one without the others would send a
	// caller to discover that by refusal.
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	info, _ := callTool[projectInfoOutput](t, session, "project_info", nil)
	for _, tool := range []string{"file_outline", "find_definition", "find_references"} {
		if !advertises(info.Capabilities, tool) {
			t.Errorf("capabilities = %v, want %s", info.Capabilities, tool)
		}
	}

	service.outlineLanguages = nil
	session = f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	info, _ = callTool[projectInfoOutput](t, session, "project_info", nil)
	for _, tool := range []string{"file_outline", "find_definition", "find_references"} {
		if advertises(info.Capabilities, tool) {
			t.Errorf("capabilities = %v; a service with no grammars must advertise none of the three", info.Capabilities)
		}
	}
}
