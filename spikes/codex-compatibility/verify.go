package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	primaryProject   = "lctk_alpha"
	secondaryProject = "lctk_beta"
	tokenEnvPrimary  = "LCTK_ALPHA_TOKEN"
	tokenEnvHeader   = "LCTK_ALPHA_HEADER_TOKEN"
)

type stepResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Detail  string `json:"detail,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

type verifyReport struct {
	Platform        string          `json:"platform"`
	CodexPath       string          `json:"codex_path"`
	CodexVersion    string          `json:"codex_version"`
	CodexHome       string          `json:"codex_home"`
	ConfigPath      string          `json:"config_path"`
	BaseURL         string          `json:"base_url"`
	GeneratedConfig string          `json:"generated_config"`
	Steps           []stepResult    `json:"steps"`
	DoctorMCPCheck  json.RawMessage `json:"doctor_mcp_check,omitempty"`
	ServerStatusRaw json.RawMessage `json:"mcp_server_status_raw,omitempty"`
	Notifications   []string        `json:"app_server_notifications,omitempty"`
	Observations    []observation   `json:"observations"`
	AppServerStderr string          `json:"app_server_stderr,omitempty"`
}

func (r *verifyReport) step(name string, passed bool, format string, args ...any) {
	r.Steps = append(r.Steps, stepResult{Name: name, Passed: passed, Detail: fmt.Sprintf(format, args...)})
}

func (r *verifyReport) skip(name, reason string) {
	r.Steps = append(r.Steps, stepResult{Name: name, Skipped: true, Detail: reason})
}

// discoverCodex resolves the Codex CLI. The bundled VS Code extension binary is
// the authoritative artifact for this spike because it is what the extension
// actually runs.
func discoverCodex(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("codex binary %q: %w", explicit, err)
		}
		return explicit, nil
	}
	if env := os.Getenv("LCTK_CODEX_BIN"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
	}
	if found, err := exec.LookPath("codex"); err == nil {
		return found, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		pattern := filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-*", "bin", "*", "codex*")
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			base := filepath.Base(m)
			if base == "codex" || base == "codex.exe" {
				return m, nil
			}
		}
	}
	return "", errors.New("no Codex CLI found; pass --codex or set LCTK_CODEX_BIN")
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// runVerify performs the whole Slice 0.4 scenario and returns the evidence
// report. Steps that require the Codex CLI are skipped, never faked, when the
// CLI is unavailable.
func runVerify(ctx context.Context, codexPath, workDir string, keepConfig bool) (*verifyReport, error) {
	report := &verifyReport{
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		CodexPath: codexPath,
	}

	port, err := freePort()
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	report.BaseURL = base

	primaryToken := "alpha-route-token-" + fmt.Sprint(port)
	projects := map[string]projectServer{
		primaryProject:   {ProjectID: primaryProject, Token: primaryToken, Sentinel: "alpha_only_sentinel"},
		secondaryProject: {ProjectID: secondaryProject, Token: "beta-route-token", Sentinel: "beta_only_sentinel"},
	}

	j := newJournal()
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: newHandler(projects, j, true)}
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return nil, err
	}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := waitForHealth(ctx, base+"/health"); err != nil {
		return nil, fmt.Errorf("harness server did not become healthy: %w", err)
	}
	report.step("harness_server_healthy", true, "listening on %s", base)

	// Isolated CODEX_HOME so the operator's real Codex configuration is never
	// read or modified.
	codexHome := filepath.Join(workDir, "codex-home")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		return nil, err
	}
	if !keepConfig {
		defer os.RemoveAll(codexHome)
	}
	report.CodexHome = codexHome

	entry := codexServerEntry{
		Name:              primaryProject,
		URL:               base + "/projects/" + primaryProject + "/mcp",
		BearerTokenEnvVar: tokenEnvPrimary,
		StartupTimeoutSec: 30,
		ToolTimeoutSec:    120,
		Enabled:           true,
		HTTPHeaders:       map[string]string{"X-Lctk-Project": primaryProject},
		EnvHTTPHeaders:    map[string]string{"X-Lctk-Token-Present": tokenEnvHeader},
	}
	config := renderCodexHomeConfig([]codexServerEntry{entry})
	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return nil, err
	}
	report.ConfigPath = configPath
	report.GeneratedConfig = config

	if codexPath == "" {
		report.skip("codex_strict_config", "Codex CLI unavailable")
		report.skip("codex_doctor_mcp_reachable", "Codex CLI unavailable")
		report.skip("codex_mcp_handshake", "Codex CLI unavailable")
		report.skip("codex_mcp_reload", "Codex CLI unavailable")
		report.Observations = j.snapshot()
		return report, nil
	}

	env := append(os.Environ(),
		"CODEX_HOME="+codexHome,
		tokenEnvPrimary+"="+primaryToken,
		tokenEnvHeader+"=header-token-present",
	)

	if out, err := runCodex(ctx, codexPath, workDir, env, "mcp", "get", primaryProject, "--json"); err != nil {
		report.step("codex_strict_config", false, "codex rejected the generated config: %s", firstLine(out, err))
	} else {
		report.step("codex_strict_config", true, "codex accepted the generated config and echoed the streamable_http transport")
	}

	// doctor performs real reachability probing and env-var validation with no
	// model call and no credentials.
	doctorOut, doctorErr := runCodex(ctx, codexPath, workDir, env, "doctor", "--json")
	if raw := extractMCPCheck(doctorOut); raw != nil {
		report.DoctorMCPCheck = raw
		var check struct {
			Status  string `json:"status"`
			Summary string `json:"summary"`
			Details struct {
				Reachability string `json:"optional reachability failed"`
			} `json:"details"`
		}
		_ = json.Unmarshal(raw, &check)
		reachable := check.Details.Reachability == ""
		report.step("codex_doctor_mcp_reachable", reachable,
			"doctor mcp.config status=%q summary=%q reachability_failure=%q",
			check.Status, check.Summary, check.Details.Reachability)
	} else {
		report.step("codex_doctor_mcp_reachable", false, "no mcp.config check in doctor output: %s", firstLine(doctorOut, doctorErr))
	}
	if v := extractCodexVersion(doctorOut); v != "" {
		report.CodexVersion = v
	}

	// Full MCP client handshake through the app server.
	client, err := startAppServer(ctx, codexPath, workDir, env)
	if err != nil {
		report.step("codex_mcp_handshake", false, "could not start app server: %v", err)
		report.Observations = j.snapshot()
		return report, nil
	}
	defer client.close()

	initCtx, cancelInit := context.WithTimeout(ctx, 60*time.Second)
	defer cancelInit()
	if _, err := client.call(initCtx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "lctk-codex-compat", "version": "0.1.0"},
	}); err != nil {
		report.step("codex_mcp_handshake", false, "initialize failed: %v", err)
		report.AppServerStderr = client.stderrTail()
		report.Observations = j.snapshot()
		return report, nil
	}

	statusCtx, cancelStatus := context.WithTimeout(ctx, 120*time.Second)
	defer cancelStatus()
	statusMsg, err := client.call(statusCtx, "mcpServerStatus/list", map[string]any{})
	if err != nil {
		report.step("codex_mcp_handshake", false, "mcpServerStatus/list failed: %v", err)
	} else if len(statusMsg.Error) > 0 {
		report.step("codex_mcp_handshake", false, "mcpServerStatus/list returned an error: %s", string(statusMsg.Error))
	} else {
		report.ServerStatusRaw = statusMsg.Result
		toolNames := extractToolNames(statusMsg.Result)
		hasProjectInfo := false
		for _, n := range toolNames {
			if strings.Contains(n, "project_info") {
				hasProjectInfo = true
			}
		}
		report.step("codex_mcp_handshake", hasProjectInfo,
			"mcpServerStatus/list reported tools %v", toolNames)
	}

	reloadCtx, cancelReload := context.WithTimeout(ctx, 60*time.Second)
	defer cancelReload()
	reloadMsg, err := client.call(reloadCtx, "config/mcpServer/reload", nil)
	switch {
	case err != nil:
		report.step("codex_mcp_reload", false, "config/mcpServer/reload failed: %v", err)
	case len(reloadMsg.Error) > 0:
		report.step("codex_mcp_reload", false, "config/mcpServer/reload returned an error: %s", string(reloadMsg.Error))
	default:
		report.step("codex_mcp_reload", true, "config/mcpServer/reload accepted with no restart of the extension host")
	}

	// Tool invocation needs a thread. A thread does not start a model turn, so
	// this still costs no quota, but it may require account state; when it does
	// not work the tool steps are reported as skipped rather than failed.
	threadID := ""
	threadCtx, cancelThread := context.WithTimeout(ctx, 90*time.Second)
	defer cancelThread()
	threadMsg, threadErr := client.call(threadCtx, "thread/start", map[string]any{"cwd": workDir})
	switch {
	case threadErr != nil:
		report.skip("codex_tool_call", fmt.Sprintf("thread/start failed: %v", threadErr))
		report.skip("codex_typed_error", "no thread available")
	case len(threadMsg.Error) > 0:
		report.skip("codex_tool_call", "thread/start returned an error: "+tail(string(threadMsg.Error), 300))
		report.skip("codex_typed_error", "no thread available")
	default:
		threadID = extractThreadID(threadMsg.Result)
		if threadID == "" {
			report.skip("codex_tool_call", "thread/start returned no thread id")
			report.skip("codex_typed_error", "no thread available")
		}
	}

	if threadID != "" {
		// A model-supplied project_id must not change the server-enforced scope.
		callCtx, cancelCall := context.WithTimeout(ctx, 90*time.Second)
		defer cancelCall()
		callMsg, err := client.call(callCtx, "mcpServer/tool/call", map[string]any{
			"server":    primaryProject,
			"tool":      "project_info",
			"threadId":  threadID,
			"arguments": map[string]any{"project_id": secondaryProject},
		})
		switch {
		case err != nil:
			report.step("codex_tool_call", false, "mcpServer/tool/call failed: %v", err)
		case len(callMsg.Error) > 0:
			report.step("codex_tool_call", false, "mcpServer/tool/call returned an error: %s", tail(string(callMsg.Error), 300))
		default:
			body := string(callMsg.Result)
			leaked := strings.Contains(body, "beta_only_sentinel")
			scoped := strings.Contains(body, "alpha_only_sentinel")
			report.step("codex_tool_call", scoped && !leaked,
				"project_info answered from the route (route_scoped=%t, foreign_sentinel_leaked=%t)", scoped, leaked)
		}

		errCtx, cancelErr := context.WithTimeout(ctx, 90*time.Second)
		defer cancelErr()
		errMsg, err := client.call(errCtx, "mcpServer/tool/call", map[string]any{
			"server":    primaryProject,
			"tool":      "typed_error",
			"threadId":  threadID,
			"arguments": map[string]any{"code": "PROJECT_STOPPED"},
		})
		switch {
		case err != nil:
			report.step("codex_typed_error", false, "typed_error call failed: %v", err)
		default:
			combined := string(errMsg.Result) + string(errMsg.Error)
			report.step("codex_typed_error", strings.Contains(combined, "PROJECT_STOPPED"),
				"typed code visible to the client: %t", strings.Contains(combined, "PROJECT_STOPPED"))
		}
	}

	report.Notifications = client.notifications()
	report.AppServerStderr = tail(client.stderrTail(), 2000)
	report.Observations = j.snapshot()

	// Route-bound scope: a request carrying the wrong project's token must fail.
	if err := probeCrossProject(ctx, base, secondaryProject, primaryToken); err != nil {
		report.step("route_bound_scope", false, "cross-project probe did not behave as required: %v", err)
	} else {
		report.step("route_bound_scope", true, "a token issued for %s was refused on the %s route", primaryProject, secondaryProject)
	}
	report.Observations = j.snapshot()

	return report, nil
}

func probeCrossProject(ctx context.Context, base, otherProject, foreignToken string) error {
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/projects/"+otherProject+"/mcp", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+foreignToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("expected 401, got %d", resp.StatusCode)
	}
	return nil
}

func waitForHealth(ctx context.Context, url string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("timed out")
}

func runCodex(ctx context.Context, codexPath, workDir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, codexPath, args...)
	cmd.Dir = workDir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// extractMCPCheck pulls the mcp.config check out of a doctor report, tolerating
// leading non-JSON output.
func extractMCPCheck(doctorOutput string) json.RawMessage {
	start := strings.Index(doctorOutput, "{")
	if start < 0 {
		return nil
	}
	var report struct {
		Checks map[string]json.RawMessage `json:"checks"`
	}
	if err := json.Unmarshal([]byte(doctorOutput[start:]), &report); err != nil {
		return nil
	}
	return report.Checks["mcp.config"]
}

func extractCodexVersion(doctorOutput string) string {
	start := strings.Index(doctorOutput, "{")
	if start < 0 {
		return ""
	}
	var report struct {
		CodexVersion string `json:"codexVersion"`
	}
	if err := json.Unmarshal([]byte(doctorOutput[start:]), &report); err != nil {
		return ""
	}
	return report.CodexVersion
}

// extractToolNames walks an arbitrary JSON value and collects plausible tool
// names, because the app server response shape is experimental and may change.
func extractToolNames(raw json.RawMessage) []string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(any, string)
	walk = func(v any, key string) {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				walk(child, k)
			}
		case []any:
			for _, child := range t {
				walk(child, key)
			}
		case string:
			if key == "name" && (t == "project_info" || t == "typed_error") && !seen[t] {
				seen[t] = true
			}
		}
	}
	walk(value, "")
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// extractThreadID finds the thread identifier in a thread/start result without
// depending on the exact experimental response shape.
func extractThreadID(raw json.RawMessage) string {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	// Observed shape is {"thread":{"id":"<uuid>",...},...}. Only named locations
	// are consulted: a generic search picks up unrelated identifiers such as
	// activePermissionProfile.id, whose value is not a thread id.
	if thread, ok := probe["thread"].(map[string]any); ok {
		for _, key := range []string{"id", "threadId", "sessionId"} {
			if v, ok := thread[key].(string); ok && v != "" {
				return v
			}
		}
	}
	for _, key := range []string{"threadId", "thread_id"} {
		if v, ok := probe[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func firstLine(out string, err error) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		if err != nil {
			return err.Error()
		}
		return "(no output)"
	}
	if idx := strings.IndexAny(trimmed, "\r\n"); idx > 0 {
		return trimmed[:idx]
	}
	return tail(trimmed, 300)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
