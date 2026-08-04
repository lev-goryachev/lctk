package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/projectstack"
)

// fakeService stands in for a project's code-intelligence container. It records
// what the gateway sent, so a test can assert what actually crosses the adapter
// boundary rather than only what comes back.
type fakeService struct {
	server   *httptest.Server
	requests []map[string]any
	response string
	status   int
	// outlineRequests records the paths asked about, so a test can assert what
	// crossed the boundary rather than only what came back.
	outlineRequests []string
	outlineResponse string
	outlineStatus   int
	// outlineLanguages is what /status advertises. Empty stands for a project whose
	// container predates the symbol layer, which is a case the gateway has to
	// handle rather than assume away.
	outlineLanguages []string
	// locateRequests records what a symbol lookup sent, so a test can assert what
	// crossed the boundary rather than only what came back.
	locateRequests []map[string]any
	locateResponse string
	locateStatus   int
}

func newFakeService(t *testing.T) *fakeService {
	t.Helper()
	service := &fakeService{
		status:           http.StatusOK,
		outlineStatus:    http.StatusOK,
		locateStatus:     http.StatusOK,
		outlineLanguages: []string{"go"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /locate", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		service.locateRequests = append(service.locateRequests, body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(service.locateStatus)
		if service.locateResponse != "" {
			_, _ = io.WriteString(w, service.locateResponse)
			return
		}
		_, _ = io.WriteString(w, `{"name":"Needle","generation":4,`+
			`"indexed_at":"2026-08-01T11:00:00Z","files":[`+
			`{"path":"internal/a.go","language":"go","digest":"abc","parsed":true,"syntax_reported":true,`+
			`"declarations":1,"occurrences":[`+
			`{"line":7,"column":6,"start_byte":40,"end_byte":46,"declaration":true,"kind":"function",`+
			`"preview":"func Needle() {}"},`+
			`{"line":12,"column":9,"start_byte":90,"end_byte":96,"container":"Other",`+
			`"preview":"\tNeedle()"}]}],`+
			`"occurrences":2,"declarations":1,"files_considered":3,"skipped_unsupported":1}`)
	})
	mux.HandleFunc("POST /outline", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		service.outlineRequests = append(service.outlineRequests, body.Path)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(service.outlineStatus)
		if service.outlineResponse != "" {
			_, _ = io.WriteString(w, service.outlineResponse)
			return
		}
		_, _ = io.WriteString(w, `{"path":"internal/a.go","language":"go","bytes":120,"lines":9,`+
			`"digest":"abc123","schema_version":1,`+
			`"symbols":[{"name":"Needle","kind":"function","start_line":7,"end_line":9,`+
			`"start_byte":40,"end_byte":118,"depth":0,"signature":"func Needle() {"}],`+
			`"syntax":{"reported":true,"valid":true}}`)
	})
	mux.HandleFunc("POST /search", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		service.requests = append(service.requests, body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(service.status)
		if service.response != "" {
			_, _ = io.WriteString(w, service.response)
			return
		}
		_, _ = io.WriteString(w, `{"matches":[{"path":"internal/a.go","line":7,"column":3,`+
			`"preview":"func Needle() {}","match":"Needle"}],`+
			`"total":1,"truncated":false,"generation":4,`+
			`"indexed_at":"2026-08-01T11:00:00Z","file_count":42}`)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		languages, _ := json.Marshal(service.outlineLanguages)
		_, _ = io.WriteString(w, `{"ready":true,"indexing":false,"generation":4,`+
			`"file_count":42,"indexed_at":"2026-08-01T11:00:00Z",`+
			`"outline_languages":`+string(languages)+`}`)
	})

	service.server = httptest.NewServer(mux)
	t.Cleanup(service.server.Close)
	return service
}

func (s *fakeService) address() string {
	return strings.TrimPrefix(s.server.URL, "http://")
}

func callExactSearch(t *testing.T, session *mcp.ClientSession, arguments map[string]any) (exactSearchOutput, *mcp.CallToolResult) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "exact_search",
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("exact_search transport failure: %v", err)
	}
	if result.IsError {
		return exactSearchOutput{}, result
	}

	var output exactSearchOutput
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("exact_search output is not the expected shape: %v", err)
	}
	return output, result
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestExactSearchAnswersWithProvenanceAndProjectRelativePaths(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output, _ := callExactSearch(t, session, map[string]any{"pattern": "Needle"})

	if output.ProjectID != "alpha-aaaaaaaa" {
		t.Errorf("project_id = %q", output.ProjectID)
	}
	if output.ScopeSource != "route_and_registry" {
		t.Errorf("scope_source = %q", output.ScopeSource)
	}
	if output.Root != projectstack.WorkspaceMount {
		t.Errorf("root = %q, want the in-container workspace", output.Root)
	}
	if len(output.Matches) != 1 || output.Matches[0].Path != "internal/a.go" {
		t.Fatalf("matches = %+v", output.Matches)
	}
	if strings.HasPrefix(output.Matches[0].Path, "/") {
		t.Error("a result path is absolute")
	}
	// ADR-0004 requires provenance and index generation on every answer.
	if output.Provenance.IndexGeneration != 4 || output.Provenance.FileCount != 42 {
		t.Errorf("provenance = %+v", output.Provenance)
	}
	if output.Provenance.Backend == "" || output.Provenance.SchemaVersion == 0 {
		t.Errorf("provenance does not identify the backend or schema: %+v", output.Provenance)
	}
}

func TestExactSearchScopeIgnoresModelSuppliedArguments(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa", "beta-bbbbbbbb")
	alpha := newFakeService(t)
	beta := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = alpha.address()
	f.service["beta-bbbbbbbb"] = beta.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output, _ := callExactSearch(t, session, map[string]any{
		"pattern":         "Needle",
		"project_id":      "beta-bbbbbbbb",
		"repository_root": "/etc",
		"path":            "/etc/passwd",
	})

	if output.ProjectID != "alpha-aaaaaaaa" {
		t.Errorf("a model-supplied project_id changed the scope: %q", output.ProjectID)
	}
	if len(beta.requests) != 0 {
		t.Errorf("the other project's service was contacted: %+v", beta.requests)
	}
	if len(alpha.requests) != 1 {
		t.Fatalf("the routed service received %d requests", len(alpha.requests))
	}
	// The scope-like arguments must not cross the adapter boundary at all: the
	// backend has no business seeing a project name it could act on.
	for _, forbidden := range []string{"project_id", "repository_root", "path"} {
		if _, present := alpha.requests[0][forbidden]; present {
			t.Errorf("%q was forwarded to the backend: %+v", forbidden, alpha.requests[0])
		}
	}
}

func TestExactSearchForwardsTheQueryFaithfully(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	callExactSearch(t, session, map[string]any{
		"pattern":        `func \w+\(`,
		"mode":           "regex",
		"case_sensitive": true,
		"path_globs":     []string{"**/*.go"},
		"languages":      []string{"Go"},
		"limit":          25,
		"cursor":         "abc",
	})

	if len(service.requests) != 1 {
		t.Fatalf("requests = %d", len(service.requests))
	}
	sent := service.requests[0]
	if sent["pattern"] != `func \w+\(` || sent["mode"] != "regex" || sent["case_sensitive"] != true {
		t.Errorf("the query was altered in transit: %+v", sent)
	}
	if sent["cursor"] != "abc" || sent["limit"] != float64(25) {
		t.Errorf("pagination was altered in transit: %+v", sent)
	}
}

func TestExactSearchSurfacesTypedBackendFailures(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantCode  string
		wantRetry bool
	}{
		{
			name:      "index still building",
			status:    http.StatusServiceUnavailable,
			body:      `{"error":{"code":"INDEX_NOT_READY","message":"The project index has not been built yet.","retryable":true}}`,
			wantCode:  "INDEX_NOT_READY",
			wantRetry: true,
		},
		{
			name:     "bad pattern",
			status:   http.StatusBadRequest,
			body:     `{"error":{"code":"INVALID_PATTERN","message":"The regular expression is invalid.","retryable":false}}`,
			wantCode: "INVALID_PATTERN",
		},
		{
			name:     "stale cursor",
			status:   http.StatusBadRequest,
			body:     `{"error":{"code":"INVALID_CURSOR","message":"The cursor belongs to another generation.","retryable":false}}`,
			wantCode: "INVALID_CURSOR",
		},
		{
			name:     "corrupt index",
			status:   http.StatusInternalServerError,
			body:     `{"error":{"code":"INDEX_CORRUPT","message":"The persistent index cannot be opened.","retryable":false}}`,
			wantCode: "INDEX_CORRUPT",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t, true, "alpha-aaaaaaaa")
			service := newFakeService(t)
			service.status = testCase.status
			service.response = testCase.body
			f.service["alpha-aaaaaaaa"] = service.address()

			session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
			_, result := callExactSearch(t, session, map[string]any{"pattern": "Needle"})
			if result == nil || !result.IsError {
				t.Fatal("a backend failure was not reported as an error result")
			}

			text := resultText(result)
			if !strings.Contains(text, testCase.wantCode) {
				t.Errorf("the typed code is not visible to the caller: %q", text)
			}
			// A caller deciding whether to try again must be told, not left to
			// infer it from the wording.
			if testCase.wantRetry && !strings.Contains(text, "retryable") {
				t.Errorf("a retryable failure does not say so: %q", text)
			}
		})
	}
}

func TestExactSearchReportsAnUnreachableService(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	// An address nothing is listening on: the project is running but its service
	// is not answering, which is a different condition from a stopped project and
	// deserves a different answer.
	f.service["alpha-aaaaaaaa"] = "127.0.0.1:1"

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	_, result := callExactSearch(t, session, map[string]any{"pattern": "Needle"})
	if result == nil || !result.IsError {
		t.Fatal("an unreachable service was not reported as an error")
	}
	if text := resultText(result); !strings.Contains(text, "SERVICE_UNAVAILABLE") {
		t.Errorf("error text = %q", text)
	}
}

func TestExactSearchOnAProjectWithoutAPublishedService(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	// No published address at all, which is what a container created from an
	// older stack definition looks like.
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	_, result := callExactSearch(t, session, map[string]any{"pattern": "Needle"})
	if result == nil || !result.IsError {
		t.Fatal("a project without a service did not report an error")
	}
	text := resultText(result)
	if !strings.Contains(text, "SEARCH_UNAVAILABLE") {
		t.Errorf("error text = %q", text)
	}
	if !strings.Contains(strings.ToLower(text), "restart") {
		t.Errorf("the error does not say how to fix it: %q", text)
	}
}

func TestProjectInfoReportsSearchCapabilityAndFreshness(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")

	// Without a published service, search must not be advertised: a caller should
	// learn a capability is absent by asking, not by a failed call.
	withoutSearch := callProjectInfo(t, f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"]), nil)
	for _, capability := range withoutSearch.Capabilities {
		if capability == "exact_search" {
			t.Error("search was advertised on a project that cannot serve it")
		}
	}
	if withoutSearch.Index != nil {
		t.Errorf("an index was reported without a service: %+v", withoutSearch.Index)
	}

	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()
	withSearch := callProjectInfo(t, f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"]), nil)

	found := false
	for _, capability := range withSearch.Capabilities {
		if capability == "exact_search" {
			found = true
		}
	}
	if !found {
		t.Errorf("capabilities = %v, want exact_search", withSearch.Capabilities)
	}
	if withSearch.Index == nil || !withSearch.Index.Ready || withSearch.Index.Generation != 4 {
		t.Errorf("index = %+v", withSearch.Index)
	}
}
