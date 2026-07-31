package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordedHeaders are the request headers whose values the journal keeps
// verbatim. Authorization is deliberately excluded; only its scheme and whether
// it matched the expected project token are recorded.
var recordedHeaders = []string{
	"Accept",
	"Content-Type",
	"Mcp-Protocol-Version",
	"Mcp-Session-Id",
	"User-Agent",
	"X-Lctk-Project",
	"X-Lctk-Token-Present",
}

// observation is one externally observable HTTP exchange against a project
// route. It is evidence about what the real Codex client sends, so it must not
// contain secret values.
type observation struct {
	Seq                  int               `json:"seq"`
	HTTPMethod           string            `json:"http_method"`
	Path                 string            `json:"path"`
	RouteProjectID       string            `json:"route_project_id"`
	Status               int               `json:"status"`
	AuthorizationScheme  string            `json:"authorization_scheme"`
	AuthorizationPresent bool              `json:"authorization_present"`
	TokenMatchedRoute    bool              `json:"token_matched_route"`
	Headers              map[string]string `json:"headers"`
	RPCMethods           []string          `json:"rpc_methods,omitempty"`
	RejectionCode        string            `json:"rejection_code,omitempty"`
}

// journal collects observations in arrival order.
type journal struct {
	mu   sync.Mutex
	seq  int
	obs  []observation
	tail func(observation)
}

func newJournal() *journal { return &journal{} }

func (j *journal) add(o observation) {
	j.mu.Lock()
	j.seq++
	o.Seq = j.seq
	j.obs = append(j.obs, o)
	tail := j.tail
	j.mu.Unlock()
	if tail != nil {
		tail(o)
	}
}

func (j *journal) snapshot() []observation {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]observation, len(j.obs))
	copy(out, j.obs)
	return out
}

// rpcMethods extracts JSON-RPC method names from a request body, tolerating a
// single object, a batch array, or a body that is not JSON-RPC at all.
func rpcMethods(body []byte) []string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	type envelope struct {
		Method string `json:"method"`
	}
	switch trimmed[0] {
	case '{':
		var one envelope
		if err := json.Unmarshal(trimmed, &one); err != nil || one.Method == "" {
			return nil
		}
		return []string{one.Method}
	case '[':
		var many []envelope
		if err := json.Unmarshal(trimmed, &many); err != nil {
			return nil
		}
		var out []string
		for _, e := range many {
			if e.Method != "" {
				out = append(out, e.Method)
			}
		}
		return out
	default:
		return nil
	}
}

func authorizationScheme(header string) string {
	if header == "" {
		return ""
	}
	if scheme, _, ok := strings.Cut(header, " "); ok {
		return scheme
	}
	return "unparsed"
}

func bearerToken(header string) string {
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}

// statusRecorder captures the response status without buffering the body, so
// streamed responses still work.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// projectServer is one LCTK-shaped project endpoint: a route-bound project
// identity plus the bearer token that the route requires.
type projectServer struct {
	ProjectID string
	Token     string
	Sentinel  string
}

type projectInfoInput struct {
	// ProjectID is a model-supplied value that must never affect scope.
	ProjectID string `json:"project_id,omitempty" jsonschema:"Model-supplied value that must not affect authoritative scope."`
}

type projectInfoOutput struct {
	ProjectID string `json:"project_id"`
	Source    string `json:"source"`
	Sentinel  string `json:"sentinel"`
}

type typedErrorInput struct {
	Code string `json:"code,omitempty" jsonschema:"Typed LCTK error code to return."`
}

// typedErrorOutput is never returned successfully; it exists so the tool has a
// concrete output type for schema generation.
type typedErrorOutput struct {
	Code string `json:"code"`
}

// newMCPServer builds the MCP server exposed on one project route. The project
// identity in every response comes from the route, never from tool arguments.
func newMCPServer(p projectServer) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "lctk-codex-compat-" + p.ProjectID,
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "project_info",
		Description: "Return the immutable server-side project scope for this route.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ projectInfoInput) (*mcp.CallToolResult, projectInfoOutput, error) {
		return nil, projectInfoOutput{
			ProjectID: p.ProjectID,
			Source:    "route_bound_server_context",
			Sentinel:  p.Sentinel,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "typed_error",
		Description: "Always fail with a typed LCTK error so error surfacing can be observed.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in typedErrorInput) (*mcp.CallToolResult, typedErrorOutput, error) {
		code := in.Code
		if code == "" {
			code = "PROJECT_STOPPED"
		}
		return nil, typedErrorOutput{}, fmt.Errorf("%s: project %s is not accepting requests", code, p.ProjectID)
	})

	return server
}

// newHandler serves every configured project under /projects/{project_id}/mcp
// and records each exchange in the journal.
func newHandler(projects map[string]projectServer, j *journal, stateless bool) http.Handler {
	ids := make([]string, 0, len(projects))
	for id := range projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for _, id := range ids {
		p := projects[id]
		inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return newMCPServer(p)
		}, &mcp.StreamableHTTPOptions{Stateless: stateless, JSONResponse: stateless})
		mux.Handle("/projects/"+p.ProjectID+"/mcp", guard(p, inner, j))
		mux.Handle("/projects/"+p.ProjectID+"/mcp/", guard(p, inner, j))
	}

	// Unknown routes are still journaled so probing attempts are visible.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		j.add(observation{
			HTTPMethod:           r.Method,
			Path:                 r.URL.Path,
			Status:               http.StatusNotFound,
			AuthorizationPresent: r.Header.Get("Authorization") != "",
			AuthorizationScheme:  authorizationScheme(r.Header.Get("Authorization")),
			Headers:              collectHeaders(r),
			RPCMethods:           rpcMethods(body),
			RejectionCode:        "PROJECT_NOT_FOUND",
		})
		writeTypedError(w, http.StatusNotFound, "PROJECT_NOT_FOUND", "no project is registered for this route")
	})

	return mux
}

func collectHeaders(r *http.Request) map[string]string {
	out := make(map[string]string)
	for _, name := range recordedHeaders {
		if v := r.Header.Get(name); v != "" {
			out[name] = v
		}
	}
	return out
}

// guard enforces the route's bearer token before the MCP handler sees the
// request. A token belonging to a different project must not grant access.
func guard(p projectServer, next http.Handler, j *journal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeTypedError(w, http.StatusBadRequest, "INVALID_REQUEST", "request body could not be read")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		auth := r.Header.Get("Authorization")
		presented := bearerToken(auth)
		matched := presented != "" &&
			subtle.ConstantTimeCompare([]byte(presented), []byte(p.Token)) == 1

		obs := observation{
			HTTPMethod:           r.Method,
			Path:                 r.URL.Path,
			RouteProjectID:       p.ProjectID,
			AuthorizationPresent: auth != "",
			AuthorizationScheme:  authorizationScheme(auth),
			TokenMatchedRoute:    matched,
			Headers:              collectHeaders(r),
			RPCMethods:           rpcMethods(body),
		}

		if !matched {
			obs.Status = http.StatusUnauthorized
			obs.RejectionCode = "GRANT_REQUIRED"
			j.add(obs)
			w.Header().Set("WWW-Authenticate", `Bearer realm="lctk"`)
			writeTypedError(w, http.StatusUnauthorized, "GRANT_REQUIRED", "the route requires a matching project grant")
			return
		}

		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		obs.Status = rec.status
		j.add(obs)
	})
}

func writeTypedError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":      code,
			"message":   message,
			"retryable": false,
		},
	})
}
