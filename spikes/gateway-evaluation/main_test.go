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
)

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"gateway-test","version":"1"}}}`

func postInitialize(t *testing.T, endpoint, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(initializeBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func requireEnvelope(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("expected status %d, got %d", status, response.StatusCode)
	}
	var envelope errorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != code || envelope.RequestID == "" {
		t.Fatalf("unexpected error envelope: %#v", envelope)
	}
}

func TestRouteBoundScopeAndGrantIsolation(t *testing.T) {
	alphaUpstream := httptest.NewServer(newUpstreamHandler("alpha"))
	defer alphaUpstream.Close()
	betaUpstream := httptest.NewServer(newUpstreamHandler("beta"))
	defer betaUpstream.Close()
	projects := newRegistry()
	projects.put(projectRecord{ID: "alpha", Token: "alpha-token", Upstream: alphaUpstream.URL + "/mcp"})
	projects.put(projectRecord{ID: "beta", Token: "beta-token", Upstream: betaUpstream.URL + "/mcp"})
	server := httptest.NewServer(newGatewayHandler("admin-token", projects))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	alpha, err := connect(ctx, server.URL+"/projects/alpha/mcp", "alpha-token")
	if err != nil {
		t.Fatal(err)
	}
	defer alpha.Close()

	tools, err := alpha.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 2 || tools.Tools[0].Name == "beta_only" || tools.Tools[1].Name == "beta_only" {
		t.Fatalf("unexpected alpha tools: %#v", tools.Tools)
	}
	result, err := alpha.CallTool(ctx, &mcp.CallToolParams{Name: "project_info", Arguments: map[string]any{
		"project_id":      "beta",
		"repository_root": "C:\\outside",
		"path":            "../../outside",
	}})
	if err != nil {
		t.Fatal(err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["project_id"] != "alpha" || structured["source"] != "server_context" {
		t.Fatalf("scope was not route-bound: %#v", result.StructuredContent)
	}

	if _, err := connect(ctx, server.URL+"/projects/beta/mcp", "alpha-token"); err == nil {
		t.Fatal("alpha grant unexpectedly connected to beta")
	}
	requireEnvelope(t, postInitialize(t, server.URL+"/projects/alpha/mcp", ""), http.StatusUnauthorized, "AUTH_REQUIRED")
	requireEnvelope(t, postInitialize(t, server.URL+"/projects/beta/mcp", "alpha-token"), http.StatusForbidden, "AUTH_FORBIDDEN")
}

func TestDynamicRegistrationAndTypedFailures(t *testing.T) {
	upstream := httptest.NewServer(newUpstreamHandler("gamma"))
	defer upstream.Close()
	projects := newRegistry()
	server := httptest.NewServer(newGatewayHandler("admin-token", projects))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPut, server.URL+"/admin/projects/gamma", strings.NewReader(`{"token":"gamma-token","upstream":"`+upstream.URL+`/mcp"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer admin-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("register status: %d", response.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gamma, err := connect(ctx, server.URL+"/projects/gamma/mcp", "gamma-token")
	if err != nil {
		t.Fatal(err)
	}
	gamma.Close()
	upstream.Close()
	requireEnvelope(t, postInitialize(t, server.URL+"/projects/gamma/mcp", "gamma-token"), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")

	request, err = http.NewRequest(http.MethodDelete, server.URL+"/admin/projects/gamma", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer admin-token")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status: %d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/projects/gamma/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.StatusCode)
	}
	var envelope errorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != "PROJECT_NOT_FOUND" || envelope.ProjectID != "gamma" || envelope.RequestID == "" {
		t.Fatalf("unexpected error envelope: %#v", envelope)
	}
}
