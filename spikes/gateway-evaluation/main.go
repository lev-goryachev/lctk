package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type projectInfoInput struct {
	ProjectID      string `json:"project_id,omitempty" jsonschema:"Model-supplied value that must not affect authoritative scope."`
	RepositoryRoot string `json:"repository_root,omitempty" jsonschema:"Model-supplied value that must not affect authoritative scope."`
	Path           string `json:"path,omitempty" jsonschema:"Model-supplied value that must not affect authoritative scope."`
}

type projectInfoOutput struct {
	ProjectID string `json:"project_id"`
	Source    string `json:"source"`
}

type sentinelInput struct{}

type sentinelOutput struct {
	ProjectID string `json:"project_id"`
	Sentinel  string `json:"sentinel"`
}

type projectRecord struct {
	ID         string    `json:"id"`
	Token      string    `json:"-"`
	Upstream   string    `json:"upstream,omitempty"`
	Tools      []string  `json:"tools,omitempty"`
	Registered time.Time `json:"registered_at"`
}

type projectRegistration struct {
	ID       string `json:"id"`
	Token    string `json:"token"`
	Upstream string `json:"upstream"`
}

type registry struct {
	mu       sync.RWMutex
	projects map[string]projectRecord
}

type errorEnvelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	ProjectID string `json:"project_id,omitempty"`
	RequestID string `json:"request_id"`
}

type benchmarkReport struct {
	Endpoint       string        `json:"endpoint"`
	Iterations     int           `json:"iterations"`
	Successes      int           `json:"successes"`
	Min            time.Duration `json:"min"`
	Median         time.Duration `json:"median"`
	P95            time.Duration `json:"p95"`
	Max            time.Duration `json:"max"`
	Mean           time.Duration `json:"mean"`
	ReconnectWorks bool          `json:"reconnect_works"`
}

func newUpstreamServer(projectID string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "lctk-spike-" + projectID, Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "project_info", Description: "Return immutable server-side project scope."}, func(_ context.Context, _ *mcp.CallToolRequest, _ projectInfoInput) (*mcp.CallToolResult, projectInfoOutput, error) {
		return nil, projectInfoOutput{ProjectID: projectID, Source: "server_context"}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: projectID + "_only", Description: "Project isolation sentinel."}, func(_ context.Context, _ *mcp.CallToolRequest, _ sentinelInput) (*mcp.CallToolResult, sentinelOutput, error) {
		return nil, sentinelOutput{ProjectID: projectID, Sentinel: projectID + "_only"}, nil
	})
	return server
}

func newUpstreamHandler(projectID string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return newUpstreamServer(projectID)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	return mux
}

func runUpstream(args []string) error {
	flags := flag.NewFlagSet("upstream", flag.ContinueOnError)
	projectID := flags.String("project", "", "immutable project identity")
	listen := flags.String("listen", "127.0.0.1:4601", "listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return errors.New("--project is required")
	}
	log.Printf("upstream %s listening on %s", *projectID, *listen)
	return http.ListenAndServe(*listen, newUpstreamHandler(*projectID))
}

func newRegistry() *registry {
	return &registry{projects: make(map[string]projectRecord)}
}

func (r *registry) get(id string) (projectRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	project, ok := r.projects[id]
	return project, ok
}

func (r *registry) put(project projectRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	project.Registered = time.Now().UTC()
	r.projects[project.ID] = project
}

func (r *registry) delete(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.projects[id]; !ok {
		return false
	}
	delete(r.projects, id)
	return true
}

func (r *registry) list() []projectRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]projectRecord, 0, len(r.projects))
	for _, project := range r.projects {
		result = append(result, project)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func constantTimeTokenEqual(actual, expected string) bool {
	if len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func requestID() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

func writeError(w http.ResponseWriter, status int, code, message, projectID string, retryable bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Code: code, Message: message, Retryable: retryable, ProjectID: projectID, RequestID: requestID()})
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, prefix))
}

func newProjectProxy(project projectRecord) (http.Handler, error) {
	target, err := url.Parse(project.Upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("invalid upstream URL %q", project.Upstream)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.URL.Path = target.Path
			request.Out.URL.RawPath = target.RawPath
			request.Out.URL.RawQuery = target.RawQuery
			request.Out.Host = target.Host
			request.Out.Header.Del("Authorization")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "The project MCP service is unavailable.", project.ID, true)
		},
	}
	return proxy, nil
}

func newGatewayHandler(adminToken string, projects *registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "projects": len(projects.list())})
	})
	mux.HandleFunc("GET /admin/projects", func(w http.ResponseWriter, r *http.Request) {
		if !constantTimeTokenEqual(bearerToken(r), adminToken) {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "A valid admin token is required.", "", false)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(projects.list())
	})
	mux.HandleFunc("PUT /admin/projects/{project_id}", func(w http.ResponseWriter, r *http.Request) {
		if !constantTimeTokenEqual(bearerToken(r), adminToken) {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "A valid admin token is required.", "", false)
			return
		}
		projectID := r.PathValue("project_id")
		var registration projectRegistration
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&registration); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "The project record is invalid.", projectID, false)
			return
		}
		if registration.ID != "" && registration.ID != projectID {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "The route and body project IDs must match.", projectID, false)
			return
		}
		if registration.Token == "" {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A project grant token is required.", projectID, false)
			return
		}
		if _, err := newProjectProxy(projectRecord{ID: projectID, Upstream: registration.Upstream}); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A valid project MCP upstream is required.", projectID, false)
			return
		}
		project := projectRecord{ID: projectID, Token: registration.Token, Upstream: registration.Upstream}
		projects.put(project)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(project)
	})
	mux.HandleFunc("DELETE /admin/projects/{project_id}", func(w http.ResponseWriter, r *http.Request) {
		if !constantTimeTokenEqual(bearerToken(r), adminToken) {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "A valid admin token is required.", "", false)
			return
		}
		projectID := r.PathValue("project_id")
		if !projects.delete(projectID) {
			writeError(w, http.StatusNotFound, "PROJECT_NOT_FOUND", "The project is not registered.", projectID, false)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/projects/{project_id}/mcp", func(w http.ResponseWriter, r *http.Request) {
		projectID := r.PathValue("project_id")
		project, ok := projects.get(projectID)
		if !ok {
			writeError(w, http.StatusNotFound, "PROJECT_NOT_FOUND", "The project is not registered.", projectID, false)
			return
		}
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "A project grant is required.", projectID, false)
			return
		}
		if !constantTimeTokenEqual(token, project.Token) {
			writeError(w, http.StatusForbidden, "AUTH_FORBIDDEN", "The client grant does not permit this project.", projectID, false)
			return
		}
		proxy, err := newProjectProxy(project)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "The project route is invalid.", projectID, false)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	return mux
}

func runGateway(args []string) error {
	flags := flag.NewFlagSet("gateway", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:4700", "listen address")
	adminToken := flags.String("admin-token", "admin-spike-token", "admin bearer token")
	bootstrap := flags.String("bootstrap", "alpha:alpha-token:http://lctk-spike-alpha:4601/mcp,beta:beta-token:http://lctk-spike-beta:4602/mcp", "comma-separated project:token:upstream records")
	if err := flags.Parse(args); err != nil {
		return err
	}
	projects := newRegistry()
	for _, value := range strings.Split(*bootstrap, ",") {
		parts := strings.SplitN(strings.TrimSpace(value), ":", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return fmt.Errorf("invalid bootstrap record %q", value)
		}
		projects.put(projectRecord{ID: parts[0], Token: parts[1], Upstream: parts[2]})
	}
	log.Printf("custom gateway listening on %s", *listen)
	return http.ListenAndServe(*listen, newGatewayHandler(*adminToken, projects))
}

func connect(ctx context.Context, endpoint, token string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "lctk-gateway-spike-client", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint}
	if token != "" {
		transport.HTTPClient = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			request = request.Clone(request.Context())
			request.Header.Set("Authorization", "Bearer "+token)
			return http.DefaultTransport.RoundTrip(request)
		})}
	}
	return client.Connect(ctx, transport, nil)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func projectInfoToolName(names []string) (string, error) {
	for _, name := range names {
		if name == "project_info" {
			return name, nil
		}
	}
	for _, name := range names {
		normalized := strings.ToLower(strings.ReplaceAll(name, "_", "-"))
		if strings.HasSuffix(normalized, "project-info") {
			return name, nil
		}
	}
	return "", fmt.Errorf("no project_info tool in %v", names)
}

func resultMap(result *mcp.CallToolResult) (map[string]any, error) {
	if structured, ok := result.StructuredContent.(map[string]any); ok {
		return structured, nil
	}
	var texts []string
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		texts = append(texts, text.Text)
		var decoded map[string]any
		if err := json.Unmarshal([]byte(text.Text), &decoded); err == nil {
			return decoded, nil
		}
	}
	if len(texts) > 0 {
		return nil, fmt.Errorf("result text: %s", strings.Join(texts, " | "))
	}
	return nil, fmt.Errorf("result has no JSON object content: %#v", result)
}

func runProbe(args []string) error {
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	endpoint := flags.String("endpoint", "", "MCP endpoint")
	token := flags.String("token", "", "bearer token")
	expectProject := flags.String("expect-project", "", "expected authoritative project")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := connect(ctx, *endpoint, *token)
	if err != nil {
		return err
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	projectInfoName, err := projectInfoToolName(names)
	if err != nil {
		return err
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: projectInfoName, Arguments: map[string]any{
		"project_id":      "attacker-selected-project",
		"repository_root": "C:\\outside",
		"path":            "../../outside",
	}})
	if err != nil {
		return err
	}
	structured, err := resultMap(result)
	if err != nil {
		return err
	}
	actualProject, _ := structured["project_id"].(string)
	if *expectProject != "" && actualProject != *expectProject {
		return fmt.Errorf("expected project %q, got %q", *expectProject, actualProject)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"endpoint": *endpoint, "tools": names, "project_info": structured})
}

func percentile(sorted []time.Duration, fraction float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * fraction)
	return sorted[index]
}

func runBench(args []string) error {
	flags := flag.NewFlagSet("bench", flag.ContinueOnError)
	endpoint := flags.String("endpoint", "", "MCP endpoint")
	token := flags.String("token", "", "bearer token")
	iterations := flags.Int("iterations", 100, "number of sequential calls")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	session, err := connect(ctx, *endpoint, *token)
	if err != nil {
		return err
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	projectInfoName, err := projectInfoToolName(names)
	if err != nil {
		return err
	}
	_, err = session.CallTool(ctx, &mcp.CallToolParams{Name: projectInfoName, Arguments: map[string]any{}})
	if err != nil {
		return fmt.Errorf("warm-up: %w", err)
	}
	durations := make([]time.Duration, 0, *iterations)
	successes := 0
	var total time.Duration
	for range *iterations {
		start := time.Now()
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: projectInfoName, Arguments: map[string]any{}})
		duration := time.Since(start)
		durations = append(durations, duration)
		total += duration
		if callErr == nil && !result.IsError {
			successes++
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	reconnect, reconnectErr := connect(ctx, *endpoint, *token)
	if reconnectErr == nil {
		_, reconnectErr = reconnect.CallTool(ctx, &mcp.CallToolParams{Name: projectInfoName, Arguments: map[string]any{}})
		_ = reconnect.Close()
	}
	report := benchmarkReport{Endpoint: *endpoint, Iterations: *iterations, Successes: successes, Min: durations[0], Median: percentile(durations, 0.5), P95: percentile(durations, 0.95), Max: durations[len(durations)-1], Mean: total / time.Duration(len(durations)), ReconnectWorks: reconnectErr == nil}
	return json.NewEncoder(os.Stdout).Encode(report)
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: harness <upstream|gateway|probe|bench>")
	}
	var err error
	switch os.Args[1] {
	case "upstream":
		err = runUpstream(os.Args[2:])
	case "gateway":
		err = runGateway(os.Args[2:])
	case "probe":
		err = runProbe(os.Args[2:])
	case "bench":
		err = runBench(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}
