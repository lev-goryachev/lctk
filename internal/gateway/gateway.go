package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/projectgrant"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

// RegistryLoader returns the current registrations. It is a function rather than
// a snapshot so a project registered while the daemon runs becomes reachable
// without a restart.
type RegistryLoader func() (*projectregistry.Registry, error)

// GrantLoader returns the current grants, for the same reason.
type GrantLoader func() (*projectgrant.Set, error)

// StatusProbe reports the runtime state of a project.
type StatusProbe func(ctx context.Context, project projectregistry.Project) (projectstack.Status, error)

// Options configures a gateway.
type Options struct {
	Registry RegistryLoader
	Grants   GrantLoader
	Status   StatusProbe
	Logger   *slog.Logger
	// Now is injectable so expiry behavior is testable.
	Now func() time.Time
	// RequireRunning gates tool calls on the project's stack being healthy. It is
	// disabled in tests that exercise routing and scope without containers.
	RequireRunning bool
	// NewSearchClient builds the adapter to a project's code-intelligence
	// service. It is injectable so search behavior can be tested against a
	// stand-in service without a container.
	NewSearchClient func(address string) *codeintel.Client
}

// Gateway serves /projects/{project_id}/mcp.
type Gateway struct {
	options Options
}

// New builds a gateway, filling in defaults.
func New(options Options) *Gateway {
	if options.Registry == nil {
		options.Registry = projectregistry.Load
	}
	if options.Grants == nil {
		options.Grants = projectgrant.Load
	}
	if options.Status == nil {
		manager := projectstack.NewManager()
		options.Status = manager.Status
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Gateway{options: options}
}

// Register attaches the project route to a mux.
func (g *Gateway) Register(mux *http.ServeMux) {
	mux.Handle("/projects/{project_id}/mcp", g)
	mux.Handle("/projects/{project_id}/mcp/", g)
}

// projectInfoInput accepts the scope-like arguments a model may invent, purely so
// they can be visibly ignored. They are declared, and documented as ignored, to
// make the contract explicit rather than leaving a model to guess.
type projectInfoInput struct {
	ProjectID      string `json:"project_id,omitempty" jsonschema:"Ignored. The authoritative project comes from the endpoint."`
	RepositoryRoot string `json:"repository_root,omitempty" jsonschema:"Ignored. The authoritative root comes from the registry."`
	Path           string `json:"path,omitempty" jsonschema:"Ignored. The authoritative root comes from the registry."`
}

// projectInfoOutput is what a calling agent needs to orient itself in a project.
type projectInfoOutput struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Profile   string `json:"profile"`
	// Root is the path as the project's own tools see it, not the host path. The
	// host path is deliberately not exposed to the client.
	Root string `json:"root"`
	// ScopeSource states where the answer came from, so a caller can verify that
	// its own arguments did not influence it.
	ScopeSource string `json:"scope_source"`
	State       string `json:"state"`
	Health      string `json:"health,omitempty"`
	Retryable   bool   `json:"retryable"`
	// Capabilities names the tools this project can actually serve right now, so
	// a caller does not have to discover by failure that search is unavailable.
	Capabilities []string `json:"capabilities"`
	ServerTime   string   `json:"server_time"`
	Version      string   `json:"version"`
	// Index describes the project's search index when there is one.
	Index *indexInfo `json:"index,omitempty"`
}

// indexInfo is the freshness ADR-0004 requires a project answer to carry.
type indexInfo struct {
	Ready      bool   `json:"ready"`
	Indexing   bool   `json:"indexing"`
	Generation uint64 `json:"generation"`
	FileCount  int    `json:"file_count"`
	IndexedAt  string `json:"indexed_at,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// exactSearchInput is the public request schema for exact_search.
//
// The scope-like fields are declared and documented as ignored for the same
// reason project_info declares them: a model will supply them, and an explicit
// "this is ignored" is a better contract than silence.
type exactSearchInput struct {
	Pattern string `json:"pattern" jsonschema:"The text or regular expression to find. Required."`
	Mode    string `json:"mode,omitempty" jsonschema:"literal (default) or regex."`

	CaseSensitive bool     `json:"case_sensitive,omitempty" jsonschema:"Match case exactly. Defaults to false."`
	PathGlobs     []string `json:"path_globs,omitempty" jsonschema:"Project-relative globs such as **/*.go. An absolute or escaping glob is refused."`
	Languages     []string `json:"languages,omitempty" jsonschema:"Restrict to languages such as Go or TypeScript."`
	Limit         int      `json:"limit,omitempty" jsonschema:"Maximum matches per page. Defaults to 50, maximum 500."`
	Cursor        string   `json:"cursor,omitempty" jsonschema:"Continue from a previous response's next_cursor. A cursor is only valid for the index generation that produced it."`

	ProjectID      string `json:"project_id,omitempty" jsonschema:"Ignored. The authoritative project comes from the endpoint."`
	RepositoryRoot string `json:"repository_root,omitempty" jsonschema:"Ignored. The authoritative root comes from the registry."`
	Path           string `json:"path,omitempty" jsonschema:"Ignored. Use path_globs to filter within the project."`
}

// exactSearchOutput is the public response schema.
type exactSearchOutput struct {
	ProjectID string            `json:"project_id"`
	Matches   []codeintel.Match `json:"matches"`
	Total     int               `json:"total"`
	Truncated bool              `json:"truncated"`
	// NextCursor is present only when more results exist.
	NextCursor string `json:"next_cursor,omitempty"`
	// ScopeSource states where the searched project came from, so a caller can
	// verify that its own arguments did not influence it.
	ScopeSource string               `json:"scope_source"`
	Root        string               `json:"root"`
	Provenance  codeintel.Provenance `json:"provenance"`
}

// serveContext is everything resolved for one authenticated request.
type serveContext struct {
	project   projectregistry.Project
	grant     projectgrant.Grant
	status    projectstack.Status
	requestID string
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	// The route is authoritative. Nothing below reads a project identifier from
	// the request body.
	projectID := r.PathValue("project_id")

	logger := g.options.Logger.With(
		slog.String("request_id", requestID),
		slog.String("project_id", projectID),
		slog.String("method", r.Method),
	)

	resolved, failure := g.resolve(r, projectID, requestID)
	if failure != nil {
		logger.Warn("project request refused",
			slog.String("code", failure.err.Code),
			slog.Bool("retryable", failure.err.Retryable))
		writeError(w, failure.status, failure.err)
		return
	}

	logger.Info("project request accepted",
		slog.String("grant_id", resolved.grant.ID),
		slog.String("client", resolved.grant.Client),
		slog.String("state", string(resolved.status.State)))

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return g.newProjectServer(*resolved)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	handler.ServeHTTP(w, r)
}

type refusal struct {
	status int
	err    TypedError
}

// resolve performs routing, authentication, and lifecycle gating in the order
// that reveals the least: a caller learns a project exists only after presenting
// a credential that covers it.
func (g *Gateway) resolve(r *http.Request, projectID, requestID string) (*serveContext, *refusal) {
	fail := func(status int, code, message, action string, retryable bool) *refusal {
		return &refusal{status: status, err: TypedError{
			Code:              code,
			Message:           message,
			Retryable:         retryable,
			RecommendedAction: action,
			ProjectID:         projectID,
			RequestID:         requestID,
		}}
	}

	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		// The stale-environment case is named here because a client that
		// inherited an environment predating its grant cannot otherwise tell it
		// apart from having no grant at all. Naming it reveals nothing: the
		// wording is identical for a project that does not exist.
		return nil, fail(http.StatusUnauthorized, CodeAuthRequired,
			"A project grant is required.",
			"Send Authorization: Bearer with this project's grant token. "+
				"If the client was started before the grant existed, start it again so it inherits the variable.", false)
	}

	grants, err := g.options.Grants()
	if err != nil {
		return nil, fail(http.StatusInternalServerError, CodeInternalError,
			"Grants could not be read.", "Inspect the LCTK home directory.", true)
	}

	now := g.options.Now()
	grant, err := grants.Resolve(token, projectID, now)
	switch {
	case errors.Is(err, projectgrant.ErrNoGrant):
		return nil, fail(http.StatusUnauthorized, CodeAuthRequired,
			"The presented credential is not a known grant.",
			"Obtain a grant with lctk grant show, or start the client again if the grant was reissued after it started.", false)
	case errors.Is(err, projectgrant.ErrGrantExpired):
		return nil, fail(http.StatusUnauthorized, CodeAuthRequired,
			"The grant has expired.", "Issue a new grant.", false)
	case errors.Is(err, projectgrant.ErrProjectNotPermitted):
		// A real credential scoped to another project. This is the case that
		// keeps one project's key from opening another.
		return nil, fail(http.StatusForbidden, CodeAuthForbidden,
			"The grant does not permit this project.",
			"Use the grant issued for this project.", false)
	case err != nil:
		return nil, fail(http.StatusInternalServerError, CodeInternalError,
			"The grant could not be validated.", "", true)
	}

	registry, err := g.options.Registry()
	if err != nil {
		return nil, fail(http.StatusInternalServerError, CodeRegistryUnavailable,
			"The project registry could not be read.",
			"Inspect the LCTK home directory.", true)
	}

	// Lookup is by exact identifier only. The convenience matching the CLI offers
	// must never establish scope for a request.
	var project projectregistry.Project
	found := false
	for _, candidate := range registry.List() {
		if candidate.ID == projectID {
			project = candidate
			found = true
			break
		}
	}
	if !found {
		return nil, fail(http.StatusNotFound, CodeProjectNotFound,
			"The project is not registered.",
			"Register it with lctk project add.", false)
	}

	status, statusErr := g.options.Status(r.Context(), project)
	if statusErr != nil {
		switch {
		case errors.Is(statusErr, projectstack.ErrLinuxContainersRequired):
			return nil, fail(http.StatusServiceUnavailable, CodeRuntimeUnsuitable,
				"The container runtime cannot run Linux containers.",
				"Switch Docker Desktop to Linux containers.", false)
		case errors.Is(statusErr, projectstack.ErrRuntimeUnavailable):
			return nil, fail(http.StatusServiceUnavailable, CodeRuntimeUnavailable,
				"The container runtime is unavailable.",
				"Start Docker Desktop.", true)
		}
	}

	if g.options.RequireRunning {
		switch status.State {
		case projectstack.StateRunning:
		case projectstack.StateStarting:
			return nil, fail(http.StatusServiceUnavailable, CodeProjectStarting,
				"The project is starting.", "Retry shortly.", true)
		case projectstack.StateStopped:
			return nil, fail(http.StatusServiceUnavailable, CodeProjectStopped,
				"The project is stopped.",
				"Start it with lctk project start.", false)
		case projectstack.StateError:
			return nil, fail(http.StatusServiceUnavailable, CodeServiceUnavailable,
				"The project stack is unhealthy.",
				"Inspect it with lctk project status.", false)
		default:
			return nil, fail(http.StatusServiceUnavailable, CodeServiceUnavailable,
				"The project state could not be determined.",
				"Inspect it with lctk project status.", true)
		}
	}

	return &serveContext{project: project, grant: grant, status: status, requestID: requestID}, nil
}

// newProjectServer builds an MCP server bound to one resolved project. The
// closure captures the project, so no tool can reach another one.
func (g *Gateway) newProjectServer(resolved serveContext) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "lctk-project-" + resolved.project.ID,
		Version: buildinfo.Version,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "project_info",
		Description: "Return the project this endpoint is bound to. " +
			"The scope comes from the endpoint and the server-side registry; " +
			"arguments naming a project or path are ignored.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ projectInfoInput) (*mcp.CallToolResult, projectInfoOutput, error) {
		output := projectInfoOutput{
			ProjectID:    resolved.project.ID,
			Name:         resolved.project.Name,
			Profile:      string(resolved.project.Profile),
			Root:         projectstack.WorkspaceMount,
			ScopeSource:  "route_and_registry",
			State:        string(resolved.status.State),
			Health:       resolved.status.Health,
			Retryable:    resolved.status.State.Retryable(),
			Capabilities: []string{"project_info"},
			ServerTime:   g.options.Now().UTC().Format(time.RFC3339),
			Version:      buildinfo.Version,
		}
		if resolved.status.ServiceAddress != "" {
			output.Capabilities = append(output.Capabilities, "exact_search")
			// The index status is reported best-effort. A project that is up but
			// whose service is still starting should still answer project_info.
			if status, err := g.searchClient(resolved).Status(ctx); err == nil {
				output.Index = &indexInfo{
					Ready:      status.Ready,
					Indexing:   status.Indexing,
					Generation: status.Generation,
					FileCount:  status.FileCount,
					IndexedAt:  status.IndexedAt,
					Reason:     status.Reason,
				}
			}
		}
		return nil, output, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "exact_search",
		Description: "Search this project's saved working tree for an exact literal or regular expression. " +
			"Results are indexed and include files that are saved but not committed. " +
			"Paths are project-relative; the scope comes from the endpoint, and arguments naming " +
			"a project or an absolute path are ignored or refused.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input exactSearchInput) (*mcp.CallToolResult, exactSearchOutput, error) {
		if resolved.status.ServiceAddress == "" {
			return nil, exactSearchOutput{}, &searchToolError{
				code:    codeintel.CodeSearchUnsupported,
				message: "This project does not expose a code-intelligence service.",
				action:  "Restart it with lctk project restart so it is recreated with the current stack definition.",
			}
		}

		response, err := g.searchClient(resolved).Search(ctx, codeintel.Request{
			Pattern:       input.Pattern,
			Mode:          input.Mode,
			CaseSensitive: input.CaseSensitive,
			PathGlobs:     input.PathGlobs,
			Languages:     input.Languages,
			Limit:         input.Limit,
			Cursor:        input.Cursor,
		})
		if err != nil {
			return nil, exactSearchOutput{}, asSearchToolError(err)
		}

		return nil, exactSearchOutput{
			ProjectID:   resolved.project.ID,
			Matches:     response.Matches,
			Total:       response.Total,
			Truncated:   response.Truncated,
			NextCursor:  response.NextCursor,
			ScopeSource: "route_and_registry",
			Root:        projectstack.WorkspaceMount,
			Provenance:  response.Provenance,
		}, nil
	})

	return server
}

// searchClient builds a client for the resolved project's published service.
func (g *Gateway) searchClient(resolved serveContext) *codeintel.Client {
	if g.options.NewSearchClient != nil {
		return g.options.NewSearchClient(resolved.status.ServiceAddress)
	}
	return codeintel.New(resolved.status.ServiceAddress)
}

// searchToolError carries a typed failure out through a tool result.
//
// A tool error reaches the model as text, so the text has to contain everything
// the model needs: what happened, whether waiting helps, and what to do. The
// alternative, an opaque "tool failed", makes an agent guess.
type searchToolError struct {
	code      string
	message   string
	action    string
	retryable bool
}

func (e *searchToolError) Error() string {
	parts := []string{e.code + ": " + e.message}
	if e.retryable {
		parts = append(parts, "This is retryable.")
	}
	if e.action != "" {
		parts = append(parts, e.action)
	}
	return strings.Join(parts, " ")
}

func asSearchToolError(err error) error {
	var typed *codeintel.Error
	if !errors.As(err, &typed) {
		return &searchToolError{code: codeintel.CodeInternalError, message: err.Error()}
	}
	return &searchToolError{
		code:      typed.Code,
		message:   typed.Message,
		action:    typed.Action,
		retryable: typed.Retryable,
	}
}

// bearerToken extracts the credential from an Authorization header, tolerating
// the case variation clients differ on.
func bearerToken(header string) string {
	scheme, rest, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}

// newRequestID produces a correlation identifier for local logs. It is not a
// security boundary, so a short random value is enough.
func newRequestID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return "req-" + base64.RawURLEncoding.EncodeToString(raw)
}
