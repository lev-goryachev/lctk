package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderCodexHomeConfigHasNoInlineSecretField(t *testing.T) {
	// Codex rejects an inline bearer_token for a streamable_http server, so the
	// generated configuration must never contain one.
	config := renderCodexHomeConfig([]codexServerEntry{{
		Name:              "lctk_alpha",
		URL:               "http://127.0.0.1:8123/projects/lctk_alpha/mcp",
		BearerTokenEnvVar: "LCTK_ALPHA_TOKEN",
		StartupTimeoutSec: 30,
		ToolTimeoutSec:    120,
		Enabled:           true,
		HTTPHeaders:       map[string]string{"X-Lctk-Project": "lctk_alpha"},
		EnvHTTPHeaders:    map[string]string{"X-Lctk-Token-Present": "LCTK_ALPHA_HEADER_TOKEN"},
	}})

	if strings.Contains(config, "bearer_token =") {
		t.Fatalf("generated config contains an inline bearer_token:\n%s", config)
	}
	for _, want := range []string{
		"[mcp_servers.lctk_alpha]",
		`url = "http://127.0.0.1:8123/projects/lctk_alpha/mcp"`,
		`bearer_token_env_var = "LCTK_ALPHA_TOKEN"`,
		"startup_timeout_sec = 30",
		"tool_timeout_sec = 120",
		"enabled = true",
		"[mcp_servers.lctk_alpha.http_headers]",
		"[mcp_servers.lctk_alpha.env_http_headers]",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("generated config is missing %q:\n%s", want, config)
		}
	}
}

func TestRenderCodexHomeConfigIsDeterministic(t *testing.T) {
	entries := []codexServerEntry{
		{Name: "zeta", URL: "http://127.0.0.1:1/z", Enabled: true, HTTPHeaders: map[string]string{"B": "2", "A": "1"}},
		{Name: "alpha", URL: "http://127.0.0.1:1/a", Enabled: true, HTTPHeaders: map[string]string{"D": "4", "C": "3"}},
	}
	first := renderCodexHomeConfig(entries)
	for i := 0; i < 5; i++ {
		if got := renderCodexHomeConfig(entries); got != first {
			t.Fatalf("render is not deterministic:\nfirst:\n%s\ngot:\n%s", first, got)
		}
	}
	if strings.Index(first, "[mcp_servers.alpha]") > strings.Index(first, "[mcp_servers.zeta]") {
		t.Error("entries are not sorted by name")
	}
}

func TestTomlEscapeHandlesWindowsPathsAndQuotes(t *testing.T) {
	// A single unescaped backslash aborts the whole Codex config load, which
	// silently removes every MCP server. Escaping is a correctness requirement.
	cases := map[string]string{
		`D:\Projets\lctk`: `"D:\\Projets\\lctk"`,
		`say "hi"`:        `"say \"hi\""`,
		"tab\there":       `"tab\there"`,
	}
	for in, want := range cases {
		if got := tomlEscape(in); got != want {
			t.Errorf("tomlEscape(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRPCMethodsParsesSingleAndBatch(t *testing.T) {
	if got := rpcMethods([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)); len(got) != 1 || got[0] != "initialize" {
		t.Errorf("single request: got %v", got)
	}
	batch := `[{"method":"tools/list"},{"method":"tools/call"},{"id":3}]`
	got := rpcMethods([]byte(batch))
	if len(got) != 2 || got[0] != "tools/list" || got[1] != "tools/call" {
		t.Errorf("batch request: got %v", got)
	}
	if got := rpcMethods([]byte("not json")); got != nil {
		t.Errorf("non-JSON body: got %v", got)
	}
	if got := rpcMethods(nil); got != nil {
		t.Errorf("empty body: got %v", got)
	}
}

func TestBearerTokenExtraction(t *testing.T) {
	if got := bearerToken("Bearer abc123"); got != "abc123" {
		t.Errorf("got %q", got)
	}
	if got := bearerToken("bearer abc123"); got != "abc123" {
		t.Errorf("scheme should be case-insensitive, got %q", got)
	}
	if got := bearerToken("Basic abc123"); got != "" {
		t.Errorf("non-bearer scheme must not yield a token, got %q", got)
	}
	if got := authorizationScheme("Bearer abc"); got != "Bearer" {
		t.Errorf("got %q", got)
	}
	if got := authorizationScheme(""); got != "" {
		t.Errorf("got %q", got)
	}
}

func newTestHandler(j *journal) http.Handler {
	return newHandler(map[string]projectServer{
		"alpha": {ProjectID: "alpha", Token: "alpha-token", Sentinel: "alpha_only"},
		"beta":  {ProjectID: "beta", Token: "beta-token", Sentinel: "beta_only"},
	}, j, true)
}

func postRPC(t *testing.T, handler http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestRouteRequiresMatchingProjectToken(t *testing.T) {
	j := newJournal()
	handler := newTestHandler(j)

	if rec := postRPC(t, handler, "/projects/alpha/mcp", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing token: got status %d, want 401", rec.Code)
	}
	// The core route-bound scope property: another project's valid token must not
	// grant access here.
	if rec := postRPC(t, handler, "/projects/alpha/mcp", "beta-token"); rec.Code != http.StatusUnauthorized {
		t.Errorf("foreign token: got status %d, want 401", rec.Code)
	}
	if rec := postRPC(t, handler, "/projects/alpha/mcp", "alpha-token"); rec.Code == http.StatusUnauthorized {
		t.Errorf("matching token was rejected with %d", rec.Code)
	}

	for _, o := range j.snapshot() {
		if o.RouteProjectID == "alpha" && o.TokenMatchedRoute && o.RejectionCode != "" {
			t.Errorf("matched request was journaled as rejected: %+v", o)
		}
	}
}

func TestUnknownRouteReturnsTypedProjectNotFound(t *testing.T) {
	j := newJournal()
	rec := postRPC(t, newTestHandler(j), "/projects/missing/mcp", "alpha-token")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", rec.Code)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if payload.Error.Code != "PROJECT_NOT_FOUND" {
		t.Errorf("got error code %q, want PROJECT_NOT_FOUND", payload.Error.Code)
	}
}

func TestJournalNeverRecordsTokenValues(t *testing.T) {
	j := newJournal()
	handler := newTestHandler(j)
	postRPC(t, handler, "/projects/alpha/mcp", "alpha-token")
	postRPC(t, handler, "/projects/alpha/mcp", "beta-token")

	encoded, err := json.Marshal(j.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"alpha-token", "beta-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("journal leaked the token %q: %s", secret, encoded)
		}
	}
}

func TestJournalRecordsRPCMethodAndRejection(t *testing.T) {
	j := newJournal()
	postRPC(t, newTestHandler(j), "/projects/alpha/mcp", "beta-token")
	obs := j.snapshot()
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(obs))
	}
	o := obs[0]
	if o.RejectionCode != "GRANT_REQUIRED" {
		t.Errorf("got rejection code %q, want GRANT_REQUIRED", o.RejectionCode)
	}
	if o.AuthorizationScheme != "Bearer" || !o.AuthorizationPresent {
		t.Errorf("authorization not recorded: %+v", o)
	}
	if o.TokenMatchedRoute {
		t.Error("foreign token must not be recorded as matching")
	}
	if len(o.RPCMethods) != 1 || o.RPCMethods[0] != "tools/list" {
		t.Errorf("rpc methods not recorded: %v", o.RPCMethods)
	}
}

func TestHealthEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	newTestHandler(newJournal()).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("got status %d, want 204", rec.Code)
	}
}

func TestProjectInfoIgnoresModelSuppliedProjectID(t *testing.T) {
	// project_info must answer from the route, never from tool arguments.
	server := newMCPServer(projectServer{ProjectID: "alpha", Token: "t", Sentinel: "alpha_only"})
	if server == nil {
		t.Fatal("newMCPServer returned nil")
	}

	j := newJournal()
	handler := newTestHandler(j)
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"project_info","arguments":{"project_id":"beta"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/projects/alpha/mcp", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer alpha-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	response, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(response), "beta_only") {
		t.Errorf("model-supplied project_id leaked another project's sentinel: %s", response)
	}
}

func TestExtractMCPCheckAndVersion(t *testing.T) {
	doctor := `warning: something
{"codexVersion":"0.146.0-alpha.9.2","checks":{"mcp.config":{"status":"warning","summary":"MCP configuration has optional issues"}}}`
	raw := extractMCPCheck(doctor)
	if raw == nil {
		t.Fatal("mcp.config check not extracted")
	}
	var check struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatal(err)
	}
	if check.Status != "warning" {
		t.Errorf("got status %q", check.Status)
	}
	if got := extractCodexVersion(doctor); got != "0.146.0-alpha.9.2" {
		t.Errorf("got version %q", got)
	}
	if extractMCPCheck("no json here") != nil {
		t.Error("expected nil for non-JSON output")
	}
}

func TestExtractToolNames(t *testing.T) {
	raw := json.RawMessage(`{"servers":[{"name":"lctk_alpha","tools":[{"name":"project_info"},{"name":"typed_error"}]}]}`)
	got := extractToolNames(raw)
	if len(got) != 2 {
		t.Fatalf("got %v, want two tool names", got)
	}
	found := map[string]bool{}
	for _, n := range got {
		found[n] = true
	}
	if !found["project_info"] || !found["typed_error"] {
		t.Errorf("got %v", got)
	}
}
