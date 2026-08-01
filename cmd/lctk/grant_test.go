package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/daemon"
	"github.com/lev-goryachev/lctk/internal/gateway"
	"github.com/lev-goryachev/lctk/internal/projectgrant"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

func grantCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runGrant(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestAddIssuesAGrantAutomatically(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	grants, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.ForProject(id, time.Now())
	if err != nil {
		t.Fatalf("registering a project did not issue a grant: %v", err)
	}
	if grant.Client != projectgrant.DefaultClient {
		t.Errorf("client = %q", grant.Client)
	}
	if !grant.Permits(id) {
		t.Error("the grant does not permit its own project")
	}
}

func TestAddReportsTheEndpoint(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	dir := makeProjectDir(t, t.TempDir(), "alpha")

	stdout, _, err := project(t, "add", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "/projects/") || !strings.Contains(stdout, "/mcp") {
		t.Errorf("output does not tell the user where to connect:\n%s", stdout)
	}
	if !strings.Contains(stdout, "grant was issued") {
		t.Errorf("output does not mention the grant:\n%s", stdout)
	}
}

// TestGrantShowWithholdsTheTokenByDefault keeps a secret out of a terminal
// transcript unless it was asked for.
func TestGrantShowWithholdsTheTokenByDefault(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	grants, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.ForProject(id, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, err := grantCommand(t, "show", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, grant.Token) {
		t.Error("the token was printed without --reveal")
	}
	if !strings.Contains(stdout, "--reveal") {
		t.Errorf("output does not say how to reveal it:\n%s", stdout)
	}

	stdout, _, err = grantCommand(t, "show", "--reveal", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, grant.Token) {
		t.Error("--reveal did not print the token")
	}
}

func TestGrantShowJSONCarriesTheConnectionContract(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	stdout, _, err := grantCommand(t, "show", "--json", "--reveal", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var view grantView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if view.Token == "" {
		t.Error("token missing under --reveal")
	}
	if !strings.Contains(view.Endpoint, "/projects/"+id+"/mcp") {
		t.Errorf("endpoint = %q", view.Endpoint)
	}
	// Slice 0.4 measured that Codex refuses an inline token, so the variable name
	// is part of the contract a client needs.
	if view.TokenEnvVar == "" {
		t.Error("no environment variable name was reported")
	}

	// Without --reveal the JSON must omit the token entirely, not send an empty
	// field a caller might mistake for a valid credential.
	stdout, _, err = grantCommand(t, "show", "--json", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, `"token"`) {
		t.Errorf("token key present without --reveal:\n%s", stdout)
	}
}

func TestGrantListRedactsTokens(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	grants, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.ForProject(id, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, err := grantCommand(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, grant.Token) {
		t.Error("listing printed a token")
	}
	if !strings.Contains(stdout, grant.ID) {
		t.Errorf("listing does not show the grant:\n%s", stdout)
	}

	stdout, _, err = grantCommand(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, grant.Token) {
		t.Error("JSON listing printed a token")
	}
}

func TestGrantRevoke(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	grants, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.ForProject(id, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := grantCommand(t, "revoke", grant.ID); err != nil {
		t.Fatal(err)
	}

	reloaded, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Resolve(grant.Token, id, time.Now()); err == nil {
		t.Error("a revoked token still resolves")
	}
	if _, _, err := grantCommand(t, "revoke", "grant-missing"); err == nil {
		t.Error("revoking an unknown grant was accepted")
	}
}

// TestRemoveRevokesTheProjectGrant keeps a credential from outliving its project.
func TestRemoveRevokesTheProjectGrant(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	grants, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.ForProject(id, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := project(t, "remove", "alpha"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Resolve(grant.Token, id, time.Now()); err == nil {
		t.Error("the grant survived removal of its only project")
	}
}

func TestGrantUsageErrors(t *testing.T) {
	isolateHome(t)
	if _, stderr, err := grantCommand(t); err == nil {
		t.Error("an empty grant subcommand was accepted")
	} else if !strings.Contains(stderr, "lctk grant show") {
		t.Errorf("usage was not printed:\n%s", stderr)
	}
	if _, _, err := grantCommand(t, "frobnicate"); err == nil {
		t.Error("an unknown grant subcommand was accepted")
	}
	if _, _, err := grantCommand(t, "show"); err == nil {
		t.Error("show without a project was accepted")
	}
	if _, _, err := grantCommand(t, "show", "missing-project"); err == nil {
		t.Error("show of an unknown project was accepted")
	}
	stdout, _, err := grantCommand(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "No grants") {
		t.Errorf("empty listing:\n%s", stdout)
	}
}

// TestEndToEndProjectEndpoint is the point of Slice 1.3: after registering a
// project, an agent can connect to its endpoint with the automatically issued
// grant and get an answer scoped to that project.
func TestEndToEndProjectEndpoint(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	alpha := addProject(t, "alpha")
	beta := addProject(t, "beta")

	// The daemon is wired with a status probe that reports the stack running, so
	// this test covers routing, grants, and scope without needing containers.
	// Lifecycle gating itself is covered in the gateway package.
	server := httptest.NewServer(daemon.NewHandlerWithGateway(gateway.Options{
		RequireRunning: true,
		Status: func(_ context.Context, p projectregistry.Project) (projectstack.Status, error) {
			return projectstack.Status{ProjectID: p.ID, State: projectstack.StateRunning, Health: "healthy"}, nil
		},
	}))
	t.Cleanup(server.Close)

	grants, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	alphaGrant, err := grants.ForProject(alpha, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	connect := func(projectID, token string) (*mcp.ClientSession, error) {
		client := mcp.NewClient(&mcp.Implementation{Name: "lctk-e2e", Version: "0.1.0"}, nil)
		transport := &mcp.StreamableClientTransport{
			Endpoint: server.URL + "/projects/" + projectID + "/mcp",
			HTTPClient: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				r.Header.Set("Authorization", "Bearer "+token)
				return http.DefaultTransport.RoundTrip(r)
			})},
		}
		return client.Connect(t.Context(), transport, nil)
	}

	session, err := connect(alpha, alphaGrant.Token)
	if err != nil {
		t.Fatalf("connecting to the project endpoint failed: %v", err)
	}
	defer session.Close()

	// A model-supplied project id must not redirect the answer.
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "project_info",
		Arguments: map[string]any{"project_id": beta},
	})
	if err != nil {
		t.Fatalf("project_info failed: %v", err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		ProjectID   string `json:"project_id"`
		ScopeSource string `json:"scope_source"`
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if output.ProjectID != alpha {
		t.Errorf("project_id = %q, want the routed project %q", output.ProjectID, alpha)
	}
	if output.ScopeSource != "route_and_registry" {
		t.Errorf("scope_source = %q", output.ScopeSource)
	}

	// Alpha's credential must be refused on beta's route.
	if _, err := connect(beta, alphaGrant.Token); err == nil {
		t.Error("alpha's grant opened beta's endpoint")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
