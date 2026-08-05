package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/gitinfo"
)

// TestEveryStageSixToolThroughMCP is a real protocol-client gate for the nine
// new actions. The service is a stand-in so the test can also inspect requests;
// the later live acceptance runs the same catalog against Docker and inference.
func TestEveryStageSixToolThroughMCP(t *testing.T) {
	requests := map[string][]map[string]any{}
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/status" {
			_, _ = io.WriteString(w, `{"ready":true,"generation":7,"graph":{"ready":true,"generation":7,"precision":"name_match","freshness":"fresh"}}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests[r.URL.Path] = append(requests[r.URL.Path], body)
		switch r.URL.Path {
		case "/graph/callers":
			_, _ = io.WriteString(w, `{"name":"Work","direction":"callers","matches":[{"path":"main.go","caller":"Run","callee":"Work","line":3,"column":2}],"total":1,"generation":7,"precision":"name_match"}`)
		case "/graph/callees":
			_, _ = io.WriteString(w, `{"name":"Run","direction":"callees","matches":[{"path":"main.go","caller":"Run","callee":"Work","line":3,"column":2}],"total":1,"generation":7,"precision":"name_match"}`)
		case "/graph/dependency-path":
			_, _ = io.WriteString(w, `{"from":"main.go","to":"dep.go","path":["main.go","dep.go"],"found":true,"max_depth":32,"generation":7,"precision":"name_match"}`)
		case "/graph/impact":
			_, _ = io.WriteString(w, `{"target":"Work","files":["main.go"],"calls":[],"total":1,"generation":7,"precision":"name_match"}`)
		case "/graph/repository-map":
			_, _ = io.WriteString(w, `{"map":"main.go\n  function Run\n","characters":25,"max_chars":512,"file_count":2,"node_count":2,"generation":7,"precision":"name_match"}`)
		case "/memory/get":
			_, _ = io.WriteString(w, memoryJSON("current"))
		case "/memory/search":
			_, _ = io.WriteString(w, `{"matches":[{"record":`+memoryJSON("old")+`}],"total":1,"modes":["semantic","lexical"],"model":"test","dimensions":16}`)
		case "/memory/put":
			commit, _ := body["source_commit"].(string)
			_, _ = io.WriteString(w, memoryJSON(commit))
		case "/memory/delete":
			_, _ = io.WriteString(w, `{"deleted":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	f := newFixture(t, true, "alpha-aaaaaaaa")
	f.service["alpha-aaaaaaaa"] = strings.TrimPrefix(service.URL, "http://")
	f.git = &fakeGit{status: gitinfo.Status{Repository: true, Branch: "main", Commit: "current"}}
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	tests := []struct {
		name      string
		arguments map[string]any
		path      string
	}{
		{"callers_find", map[string]any{"name": "Work", "project_id": "wrong"}, "/graph/callers"},
		{"callees_find", map[string]any{"name": "Run"}, "/graph/callees"},
		{"dependency_path", map[string]any{"from": "main.go", "to": "dep.go"}, "/graph/dependency-path"},
		{"impact_analyze", map[string]any{"target": "Work"}, "/graph/impact"},
		{"repository_map", map[string]any{"max_chars": 512}, "/graph/repository-map"},
		{"memory_get", map[string]any{"key": "architecture/retry"}, "/memory/get"},
		{"memory_search", map[string]any{"query": "retry"}, "/memory/search"},
		{"memory_put", map[string]any{"key": "architecture/retry", "kind": "decision", "content": "Retry.", "confidence": 0.9}, "/memory/put"},
		{"memory_delete", map[string]any{"key": "architecture/retry", "expected_revision": 1}, "/memory/delete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: test.arguments})
			if err != nil || result.IsError {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			encoded, _ := json.Marshal(result.StructuredContent)
			var output map[string]any
			if err := json.Unmarshal(encoded, &output); err != nil {
				t.Fatal(err)
			}
			if output["project_id"] != "alpha-aaaaaaaa" || output["scope_source"] != "route_and_registry" {
				t.Fatalf("scope output = %s", encoded)
			}
			if test.name == "memory_search" {
				stale, ok := output["stale_keys"].([]any)
				if !ok || len(stale) != 1 || stale[0] != "architecture/retry" {
					t.Fatalf("stale_keys = %v, want architecture/retry", output["stale_keys"])
				}
			}
			if len(requests[test.path]) != 1 {
				t.Fatalf("requests to %s = %d", test.path, len(requests[test.path]))
			}
		})
	}
	if requests["/memory/put"][0]["source_commit"] != "current" {
		t.Fatalf("memory_put source_commit = %v, want current", requests["/memory/put"][0]["source_commit"])
	}
}

func memoryJSON(commit string) string {
	return `{"key":"architecture/retry","kind":"decision","content":"Retry.","confidence":0.9,` +
		`"provenance":["docs/adr/retry.md"],"source_commit":"` + commit + `","revision":1,` +
		`"created_at":"2026-08-04T00:00:00Z","updated_at":"2026-08-04T00:00:00Z","review_due":false,"low_confidence":false}`
}
