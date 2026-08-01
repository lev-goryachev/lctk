package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/adminsession"
	"github.com/lev-goryachev/lctk/internal/dockerapi"
	"github.com/lev-goryachev/lctk/internal/hostsettings"
	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/logring"
	"github.com/lev-goryachev/lctk/internal/projectgrant"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

var testNow = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// stack stands in for the container runtime and records what it was asked to do.
type stack struct {
	state   projectstack.State
	actions []string
}

func (s *stack) Status(context.Context, projectregistry.Project) (projectstack.Status, error) {
	return projectstack.Status{State: s.state}, nil
}

func (s *stack) Start(_ context.Context, p projectregistry.Project, _ time.Duration) (projectstack.Status, error) {
	s.actions = append(s.actions, "start:"+p.ID)
	s.state = projectstack.StateRunning
	return projectstack.Status{State: s.state}, nil
}

func (s *stack) Stop(_ context.Context, p projectregistry.Project) (projectstack.Status, error) {
	s.actions = append(s.actions, "stop:"+p.ID)
	s.state = projectstack.StateStopped
	return projectstack.Status{State: s.state}, nil
}

func (s *stack) Restart(_ context.Context, p projectregistry.Project, _ time.Duration) (projectstack.Status, error) {
	s.actions = append(s.actions, "restart:"+p.ID)
	return projectstack.Status{State: projectstack.StateRunning}, nil
}

type fixture struct {
	server   *httptest.Server
	sessions *adminsession.Store
	stack    *stack
	grants   *projectgrant.Set
	csrf     string
	client   *http.Client
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv(lctkhome.EnvOverride, home)

	registry := registryWith(t, home, projectregistry.Project{
		ID: "alpha-aaaaaaaa", Name: "alpha", Path: filepath.Join(home, "alpha"),
		Key: "alpha", Profile: projectregistry.ProfileMinimal, RegisteredAt: testNow,
	})

	grants := projectgrant.New()
	if _, err := grants.Issue("codex", []string{"alpha-aaaaaaaa"}, time.Time{}, testNow); err != nil {
		t.Fatal(err)
	}

	sessions, err := adminsession.New(adminsession.Options{Path: filepath.Join(home, adminsession.FileName)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close() })

	runtimeStack := &stack{state: projectstack.StateStopped}
	server := New(Options{
		Sessions: sessions,
		Registry: func() (*projectregistry.Registry, error) { return registry, nil },
		Grants:   func() (*projectgrant.Set, error) { return grants, nil },
		Stack:    runtimeStack,
		Settings: func() (hostsettings.Settings, error) { return hostsettings.Defaults, nil },
		Probe: func(context.Context) (dockerapi.Status, error) {
			return dockerapi.Status{APIVersion: "1.51", OSType: "linux"}, nil
		},
		Logs: func() []logring.Record {
			return []logring.Record{{At: testNow, Level: "INFO", Message: "watching project"}}
		},
		Now: func() time.Time { return testNow },
	})

	mux := http.NewServeMux()
	server.Register(mux)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	return &fixture{
		server: httpServer, sessions: sessions, stack: runtimeStack, grants: grants,
		client: &http.Client{},
	}
}

func registryWith(t *testing.T, home string, projects ...projectregistry.Project) *projectregistry.Registry {
	t.Helper()
	document := struct {
		SchemaVersion int                       `json:"schema_version"`
		Projects      []projectregistry.Project `json:"projects"`
	}{SchemaVersion: projectregistry.SchemaVersion, Projects: projects}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, projectregistry.FileName), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := projectregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func (f *fixture) do(t *testing.T, method, path string, body string) (*http.Response, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if f.csrf != "" && method != http.MethodGet {
		request.Header.Set(adminsession.HeaderCSRF, f.csrf)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })

	var decoded map[string]any
	buffer := &bytes.Buffer{}
	_, _ = buffer.ReadFrom(response.Body)
	_ = json.Unmarshal(buffer.Bytes(), &decoded)
	return response, decoded
}

// signIn exchanges the code and keeps the cookie, the way a browser would.
func (f *fixture) signIn(t *testing.T) {
	t.Helper()
	// A real cookie jar, so the test exercises what a browser would actually
	// send back rather than a hand-built header.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.client.Jar = jar

	code := f.sessions.Code()
	response, body := f.do(t, http.MethodPost, "/admin/session", `{"code":"`+code+`"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sign in returned %d: %v", response.StatusCode, body)
	}
	csrf, _ := body["csrf"].(string)
	if csrf == "" {
		t.Fatal("sign in returned no CSRF token")
	}
	f.csrf = csrf
}

func TestEveryEndpointRefusesWithoutASession(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/admin/api/overview", "/admin/api/projects", "/admin/api/grants", "/admin/api/logs"} {
		response, _ := f.do(t, http.MethodGet, path, "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a session returned %d, want 401", path, response.StatusCode)
		}
	}
	response, _ := f.do(t, http.MethodPost, "/admin/api/projects/alpha-aaaaaaaa/start", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unauthenticated start returned %d, want 401", response.StatusCode)
	}
	if len(f.stack.actions) != 0 {
		t.Fatalf("an unauthenticated request reached the container runtime: %v", f.stack.actions)
	}
}

// The credential that opens a project must not open the machine. This is the
// separation the whole surface is built around.
func TestAProjectGrantDoesNotOpenTheAdminSurface(t *testing.T) {
	f := newFixture(t)
	token := f.grants.List()[0].Token

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.server.URL+"/admin/api/projects", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a project grant reached the admin surface: %d", response.StatusCode)
	}
}

func TestASignedInAdministratorSeesTheProjects(t *testing.T) {
	f := newFixture(t)
	f.signIn(t)

	response, body := f.do(t, http.MethodGet, "/admin/api/projects", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("projects returned %d: %v", response.StatusCode, body)
	}
	projects, _ := body["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("projects = %v, want one", projects)
	}
	first, _ := projects[0].(map[string]any)
	if first["id"] != "alpha-aaaaaaaa" {
		t.Fatalf("project = %v", first)
	}
	if first["mode"] != string(hostsettings.ModeNormal) {
		t.Fatalf("mode = %v, want the machine policy", first["mode"])
	}
}

// A cross-origin page cannot read the CSRF token, so requiring it in a header is
// what stops such a page from administering LCTK through the user's browser.
func TestAStateChangingRequestWithoutTheCsrfHeaderIsRefused(t *testing.T) {
	f := newFixture(t)
	f.signIn(t)

	held := f.csrf
	f.csrf = ""
	response, _ := f.do(t, http.MethodPost, "/admin/api/projects/alpha-aaaaaaaa/start", "")
	f.csrf = held

	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a request without the CSRF header returned %d, want 401", response.StatusCode)
	}
	if len(f.stack.actions) != 0 {
		t.Fatalf("it reached the container runtime anyway: %v", f.stack.actions)
	}
}

func TestLifecycleActionsReachTheRuntime(t *testing.T) {
	f := newFixture(t)
	f.signIn(t)

	for _, action := range []string{"start", "restart", "stop"} {
		response, body := f.do(t, http.MethodPost, "/admin/api/projects/alpha-aaaaaaaa/"+action, "")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s returned %d: %v", action, response.StatusCode, body)
		}
	}
	want := []string{"start:alpha-aaaaaaaa", "restart:alpha-aaaaaaaa", "stop:alpha-aaaaaaaa"}
	if strings.Join(f.stack.actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", f.stack.actions, want)
	}
}

// A project is addressed by its exact identifier. The prefix matching the CLI
// offers must not decide which project a button press affects.
func TestAnUnknownProjectIsNotMatchedByPrefix(t *testing.T) {
	f := newFixture(t)
	f.signIn(t)

	response, _ := f.do(t, http.MethodPost, "/admin/api/projects/alpha/start", "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("a prefix reached a project: %d", response.StatusCode)
	}
}

func TestTheResourceModeCanBeSetAndCleared(t *testing.T) {
	f := newFixture(t)
	f.signIn(t)

	response, body := f.do(t, http.MethodPost, "/admin/api/projects/alpha-aaaaaaaa/mode", `{"mode":"quiet"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("setting the mode returned %d: %v", response.StatusCode, body)
	}
	if body["note"] == "" {
		t.Error("the answer did not say that a restart is needed")
	}

	_, body = f.do(t, http.MethodGet, "/admin/api/projects", "")
	projects, _ := body["projects"].([]any)
	first, _ := projects[0].(map[string]any)
	if first["mode"] != "quiet" {
		t.Fatalf("mode = %v, want quiet", first["mode"])
	}

	if response, _ := f.do(t, http.MethodPost,
		"/admin/api/projects/alpha-aaaaaaaa/mode", `{"mode":"turbo"}`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown mode returned %d, want 400", response.StatusCode)
	}
}

// The surface exists to manage grants, not to hand them out. A page that
// displayed a token would leave it in a screenshot and a browser cache.
func TestGrantsAreListedWithoutTheirTokens(t *testing.T) {
	f := newFixture(t)
	f.signIn(t)

	response, body := f.do(t, http.MethodGet, "/admin/api/grants", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("grants returned %d", response.StatusCode)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	token := f.grants.List()[0].Token
	if strings.Contains(string(encoded), token) {
		t.Fatal("a grant token was served to the admin page")
	}
	if !strings.Contains(string(encoded), "codex") {
		t.Fatalf("the grant was not listed at all: %s", encoded)
	}
}

func TestAGrantCanBeRevoked(t *testing.T) {
	f := newFixture(t)
	f.signIn(t)

	id := f.grants.List()[0].ID
	response, body := f.do(t, http.MethodDelete, "/admin/api/grants/"+id, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke returned %d: %v", response.StatusCode, body)
	}
	if !f.grants.List()[0].Revoked {
		t.Fatal("the grant was not revoked")
	}
}

func TestTheOverviewCarriesTheRuntimeDiagnostic(t *testing.T) {
	f := newFixture(t)
	f.signIn(t)

	_, body := f.do(t, http.MethodGet, "/admin/api/overview", "")
	runtime, _ := body["runtime"].(map[string]any)
	if runtime["available"] != true || runtime["api_version"] != "1.51" {
		t.Fatalf("runtime = %v, want the probe's answer", runtime)
	}
	if body["version"] == "" {
		t.Error("the overview carries no version")
	}
}

func TestThePageIsServedWithoutASession(t *testing.T) {
	f := newFixture(t)
	response, err := f.client.Get(f.server.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("the page returned %d; it has to load in order to sign in", response.StatusCode)
	}
	if policy := response.Header.Get("Content-Security-Policy"); !strings.Contains(policy, "default-src 'none'") {
		t.Errorf("the page is served without a restrictive policy: %q", policy)
	}
}
