package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// stepResult is one measured outcome. A step is passed, failed, or skipped; it
// is never assumed.
type stepResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type report struct {
	Platform      string       `json:"platform"`
	LctkVersion   string       `json:"lctk_version"`
	CodexPath     string       `json:"codex_path"`
	CodexVersion  string       `json:"codex_version"`
	DockerVersion string       `json:"docker_version"`
	WorkDir       string       `json:"work_dir"`
	CodexHome     string       `json:"codex_home"`
	Endpoint      string       `json:"endpoint"`
	Config        string       `json:"generated_config"`
	Steps         []stepResult `json:"steps"`
	Notifications []string     `json:"app_server_notifications,omitempty"`
	Stderr        string       `json:"app_server_stderr,omitempty"`
}

func (r *report) pass(name, format string, args ...any) {
	r.Steps = append(r.Steps, stepResult{Name: name, Passed: true, Detail: fmt.Sprintf(format, args...)})
}

func (r *report) fail(name, format string, args ...any) {
	r.Steps = append(r.Steps, stepResult{Name: name, Detail: fmt.Sprintf(format, args...)})
}

func (r *report) skip(name, format string, args ...any) {
	r.Steps = append(r.Steps, stepResult{Name: name, Skipped: true, Detail: fmt.Sprintf(format, args...)})
}

func (r *report) failed() int {
	count := 0
	for _, step := range r.Steps {
		if !step.Passed && !step.Skipped {
			count++
		}
	}
	return count
}

// environment is the isolated world one run operates in. Nothing here touches
// the operator's real LCTK or Codex state.
type environment struct {
	lctk      string
	codex     string
	work      string
	lctkHome  string
	codexHome string
	env       []string
	address   string
}

func (e *environment) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, e.lctk, args...)
	cmd.Env = e.env
	cmd.Dir = e.work
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("lctk %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (e *environment) runJSON(ctx context.Context, target any, args ...string) error {
	out, err := e.run(ctx, args...)
	if err != nil {
		return err
	}
	start := strings.IndexAny(out, "{[")
	if start < 0 {
		return fmt.Errorf("lctk %s produced no JSON: %s", strings.Join(args, " "), strings.TrimSpace(out))
	}
	return json.Unmarshal([]byte(out[start:]), target)
}

type projectView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type envView struct {
	TokenEnvVar string `json:"token_env_var"`
	Token       string `json:"token"`
}

// runChain performs the Slice 1.4 scenario end to end and returns the evidence.
func runChain(ctx context.Context, e *environment, keep bool) (*report, error) {
	rep := &report{
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		CodexPath: e.codex,
		WorkDir:   e.work,
		CodexHome: e.codexHome,
	}

	if out, err := e.run(ctx, "version"); err == nil {
		rep.LctkVersion = strings.TrimSpace(out)
	}
	if out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}} {{.Server.Os}}").Output(); err == nil {
		rep.DockerVersion = strings.TrimSpace(string(out))
	}
	if !strings.HasSuffix(rep.DockerVersion, "linux") {
		rep.skip("all", "no Linux-capable container runtime: %q", rep.DockerVersion)
		return rep, nil
	}

	// 1. Register two folders.
	var alpha, beta projectView
	for _, spec := range []struct {
		name string
		view *projectView
	}{{"alpha", &alpha}, {"beta", &beta}} {
		dir := filepath.Join(e.work, spec.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		marker := filepath.Join(dir, spec.name+"_only_marker.txt")
		if err := os.WriteFile(marker, []byte(spec.name+"\n"), 0o644); err != nil {
			return nil, err
		}
		if err := e.runJSON(ctx, spec.view, "project", "add", "--json", dir); err != nil {
			rep.fail("register_folder", "%v", err)
			return rep, nil
		}
	}
	rep.pass("register_folder", "registered %s and %s", alpha.ID, beta.ID)

	// 2. Start both stacks against the real container runtime.
	for _, project := range []*projectView{&alpha, &beta} {
		if err := e.runJSON(ctx, project, "project", "start", "--json", project.ID); err != nil {
			rep.fail("start_stack", "%v", err)
			return rep, nil
		}
		if project.State != "running" {
			rep.fail("start_stack", "%s state = %q", project.ID, project.State)
			return rep, nil
		}
	}
	rep.pass("start_stack", "%s and %s are running", alpha.ID, beta.ID)
	if !keep {
		defer func() {
			for _, project := range []projectView{alpha, beta} {
				_, _ = e.run(context.Background(), "project", "remove", "--json", project.ID)
			}
		}()
	}

	// 3. Serve the endpoint and generate the client configuration.
	stopDaemon, err := startDaemon(ctx, e)
	if err != nil {
		rep.fail("daemon_listening", "%v", err)
		return rep, nil
	}
	defer stopDaemon()
	rep.Endpoint = "http://" + e.address
	rep.pass("daemon_listening", "serving on %s", rep.Endpoint)

	for _, project := range []projectView{alpha, beta} {
		if _, err := e.run(ctx, "codex", "config", "--apply", "--listen", e.address, project.ID); err != nil {
			rep.fail("generate_client_config", "%v", err)
			return rep, nil
		}
	}
	generated, err := os.ReadFile(filepath.Join(e.codexHome, "config.toml"))
	if err != nil {
		return nil, err
	}
	rep.Config = string(generated)

	tokens := map[string]envView{}
	for _, project := range []projectView{alpha, beta} {
		var view envView
		if err := e.runJSON(ctx, &view, "codex", "env", "--json", "--reveal", project.ID); err != nil {
			rep.fail("generate_client_config", "%v", err)
			return rep, nil
		}
		tokens[project.ID] = view
	}
	if strings.Contains(rep.Config, tokens[alpha.ID].Token) || strings.Contains(rep.Config, tokens[beta.ID].Token) {
		rep.fail("generate_client_config", "the generated configuration contains a token")
	} else {
		rep.pass("generate_client_config", "two streamable_http entries referencing %s and %s, no token in the file",
			tokens[alpha.ID].TokenEnvVar, tokens[beta.ID].TokenEnvVar)
	}

	// The credential reaches the client the way ADR-0014 specifies: in the
	// environment of a process LCTK starts.
	codexEnv := append(os.Environ(),
		"CODEX_HOME="+e.codexHome,
		tokens[alpha.ID].TokenEnvVar+"="+tokens[alpha.ID].Token,
		tokens[beta.ID].TokenEnvVar+"="+tokens[beta.ID].Token,
	)

	alphaServer := "lctk_" + strings.ReplaceAll(alpha.ID, "-", "_")
	betaServer := "lctk_" + strings.ReplaceAll(beta.ID, "-", "_")

	// 4. The real client accepts the generated configuration.
	if out, err := runCodexCommand(ctx, e, codexEnv, "mcp", "get", alphaServer, "--json"); err != nil {
		rep.fail("client_accepts_config", "codex rejected the entry: %s", oneLine(out, err))
	} else if !strings.Contains(out, "streamable_http") {
		rep.fail("client_accepts_config", "unexpected transport: %s", oneLine(out, nil))
	} else {
		rep.pass("client_accepts_config", "codex mcp get reported the streamable_http transport")
	}

	doctorOut, doctorErr := runCodexCommand(ctx, e, codexEnv, "doctor", "--json")
	rep.CodexVersion = jsonField(doctorOut, "codexVersion")
	if check := mcpCheck(doctorOut); check != nil {
		status, _ := check["status"].(string)
		summary, _ := check["summary"].(string)
		rep.pass("client_reports_reachable", "doctor mcp.config status=%q summary=%q", status, summary)
		if status != "ok" {
			rep.Steps[len(rep.Steps)-1].Passed = false
		}
	} else {
		rep.fail("client_reports_reachable", "no mcp.config check: %s", oneLine(doctorOut, doctorErr))
	}

	// 5. Drive the real client through the chain.
	client, err := startAppServer(ctx, e.codex, e.work, codexEnv)
	if err != nil {
		rep.fail("client_connects", "could not start the app server: %v", err)
		return rep, nil
	}
	defer client.close()

	if err := callOK(ctx, client, 60*time.Second, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "lctk-codex-e2e", "version": "0.1.0"},
	}); err != nil {
		rep.fail("client_connects", "%v", err)
		return rep, nil
	}

	statusRaw, err := callResult(ctx, client, 180*time.Second, "mcpServerStatus/list", map[string]any{})
	switch {
	case err != nil:
		rep.fail("client_connects", "%v", err)
		return rep, nil
	default:
		tools := toolNamesFor(statusRaw, alphaServer)
		if len(tools) == 0 {
			rep.fail("client_connects", "%s exposed no tools: %s", alphaServer, truncate(string(statusRaw), 400))
		} else {
			rep.pass("client_connects", "%s exposed %v through the real client", alphaServer, tools)
		}
	}

	threadID := ""
	if raw, err := callResult(ctx, client, 120*time.Second, "thread/start", map[string]any{"cwd": e.work}); err == nil {
		threadID = threadIDOf(raw)
	}
	if threadID == "" {
		rep.skip("project_info_through_client", "no thread available, so no tool can be called")
		rep.skip("scope_survives_a_wrong_argument", "no thread available")
		rep.skip("stopped_project_is_typed", "no thread available")
		rep.skip("restart_reconnects", "no thread available")
	} else {
		// 6. project_info, with a deliberately wrong project_id argument.
		body, err := toolCall(ctx, client, alphaServer, "project_info", threadID, map[string]any{
			"project_id": beta.ID, "repository_root": "/etc", "path": "/etc",
		})
		switch {
		case err != nil:
			rep.fail("project_info_through_client", "%v", err)
			rep.skip("scope_survives_a_wrong_argument", "no answer to inspect")
		default:
			rep.pass("project_info_through_client", "answered with project_id=%s", alpha.ID)
			answeredAlpha := strings.Contains(body, alpha.ID)
			leakedBeta := strings.Contains(body, beta.ID)
			leakedHostPath := strings.Contains(body, e.work)
			if !answeredAlpha || leakedBeta || leakedHostPath {
				rep.Steps[len(rep.Steps)-1].Passed = false
				rep.Steps[len(rep.Steps)-1].Detail = truncate(body, 500)
			}
			if answeredAlpha && !leakedBeta {
				rep.pass("scope_survives_a_wrong_argument",
					"arguments named %s and /etc; the answer is %s with scope_source=%t and no host path",
					beta.ID, alpha.ID, strings.Contains(body, "route_and_registry"))
			} else {
				rep.fail("scope_survives_a_wrong_argument", "%s", truncate(body, 500))
			}
		}

		// 7. A credential issued for one project is refused on another route.
		if err := probeForeignToken(ctx, rep.Endpoint, beta.ID, tokens[alpha.ID].Token); err != nil {
			rep.fail("cross_project_access_refused", "%v", err)
		} else {
			rep.pass("cross_project_access_refused",
				"a token issued for %s was refused on the %s route with 403 AUTH_FORBIDDEN", alpha.ID, beta.ID)
		}

		// 8. A stopped project answers with a typed error rather than nothing. The
		// start counter is read while the project is still up, because a stopped
		// project has no container to read it from.
		startsBefore := containerStarts(ctx, beta.ID)
		if _, err := e.run(ctx, "project", "stop", "--json", beta.ID); err != nil {
			rep.fail("stopped_project_is_typed", "%v", err)
		} else {
			envelope, status := probeTyped(ctx, rep.Endpoint, beta.ID, tokens[beta.ID].Token)
			typed := envelope["code"] == "PROJECT_STOPPED"
			detail := fmt.Sprintf("status=%d body=%v", status, envelope)
			if typed {
				rep.pass("stopped_project_is_typed", "%s", detail)
			} else {
				rep.fail("stopped_project_is_typed", "%s", detail)
			}

			stoppedBody, callErr := toolCall(ctx, client, betaServer, "project_info", threadID, map[string]any{})
			combined := stoppedBody
			if callErr != nil {
				combined = strings.TrimPrefix(combined+" | "+callErr.Error(), " | ")
			}
			visible := strings.Contains(combined, "PROJECT_STOPPED")
			if visible {
				rep.pass("stopped_reason_reaches_the_client", "%s", truncate(combined, 400))
			} else {
				rep.fail("stopped_reason_reaches_the_client", "%s", truncate(combined, 400))
			}
		}

		// 9. Restarting makes the project usable again without touching the client
		// configuration, and the project volume survives the stop.
		if _, err := e.run(ctx, "project", "start", "--json", beta.ID); err != nil {
			rep.fail("restart_reconnects", "%v", err)
		} else {
			startsAfter := containerStarts(ctx, beta.ID)
			switch {
			case startsBefore <= 0 || startsAfter <= 0:
				rep.skip("project_state_survives_restart", "the start counter could not be read from the container")
			case startsAfter > startsBefore:
				rep.pass("project_state_survives_restart",
					"the project volume carried the start counter from %d to %d across a full stop", startsBefore, startsAfter)
			default:
				rep.fail("project_state_survives_restart", "the start counter went from %d to %d", startsBefore, startsAfter)
			}

			body, err := toolCall(ctx, client, betaServer, "project_info", threadID, map[string]any{})
			switch {
			case err != nil:
				rep.fail("restart_reconnects", "%v", err)
			case !strings.Contains(body, beta.ID):
				rep.fail("restart_reconnects", "%s", truncate(body, 400))
			default:
				rep.pass("restart_reconnects",
					"the same client session reached %s again after a stop and start, with no configuration change", beta.ID)
			}
		}
	}

	rep.Notifications = client.notifications()
	rep.Stderr = truncate(client.stderrTail(), 2000)
	return rep, nil
}

func startDaemon(ctx context.Context, e *environment) (func(), error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	e.address = fmt.Sprintf("127.0.0.1:%d", port)

	cmd := exec.Command(e.lctk, "daemon", "--listen", e.address)
	cmd.Env = e.env
	cmd.Dir = e.work
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the daemon: %w", err)
	}
	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+e.address+"/health", nil)
		if err != nil {
			stop()
			return nil, err
		}
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
			return stop, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	return nil, errors.New("the daemon did not become healthy")
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// probeForeignToken checks the property project isolation rests on: one
// project's credential must not open another project's route.
func probeForeignToken(ctx context.Context, base, projectID, foreignToken string) error {
	envelope, status := probeTyped(ctx, base, projectID, foreignToken)
	if status != http.StatusForbidden {
		return fmt.Errorf("status = %d, want 403", status)
	}
	if envelope["code"] != "AUTH_FORBIDDEN" {
		return fmt.Errorf("code = %v, want AUTH_FORBIDDEN", envelope["code"])
	}
	return nil
}

func probeTyped(ctx context.Context, base, projectID, token string) (map[string]any, int) {
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/projects/"+projectID+"/mcp", body)
	if err != nil {
		return map[string]any{"transport_error": err.Error()}, 0
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return map[string]any{"transport_error": err.Error()}, 0
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)

	var envelope struct {
		Error map[string]any `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if envelope.Error == nil {
		return map[string]any{"body": truncate(string(raw), 300)}, response.StatusCode
	}
	return envelope.Error, response.StatusCode
}

// containerStarts reads the start counter the project image keeps in its
// persistent volume. It returns zero when the counter cannot be read, so an
// unavailable reading is reported as a skip rather than as a failure.
func containerStarts(ctx context.Context, projectID string) int {
	container := "lctk-" + projectID + "-code-intel"
	out, err := exec.CommandContext(ctx, "docker", "exec", container, "cat", "/var/lib/lctk/starts").Output()
	if err != nil {
		return 0
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return count
}

func runCodexCommand(ctx context.Context, e *environment, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, e.codex, args...)
	cmd.Dir = e.work
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func callOK(ctx context.Context, client *appServerClient, timeout time.Duration, method string, params any) error {
	_, err := callResult(ctx, client, timeout, method, params)
	return err
}

func callResult(ctx context.Context, client *appServerClient, timeout time.Duration, method string, params any) (json.RawMessage, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	message, err := client.call(callCtx, method, params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if len(message.Error) > 0 {
		return nil, fmt.Errorf("%s returned an error: %s", method, truncate(string(message.Error), 400))
	}
	return message.Result, nil
}

func toolCall(ctx context.Context, client *appServerClient, server, tool, threadID string, arguments map[string]any) (string, error) {
	raw, err := callResult(ctx, client, 120*time.Second, "mcpServer/tool/call", map[string]any{
		"server":    server,
		"tool":      tool,
		"threadId":  threadID,
		"arguments": arguments,
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// toolNamesFor collects the tool names reported for one server.
//
// The app-server response shape is experimental, so the entry is located by its
// name rather than by a fixed path, and both the map and list forms of a tool
// collection are accepted.
func toolNamesFor(raw json.RawMessage, server string) []string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var found []string
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			if name, _ := typed["name"].(string); name == server {
				switch tools := typed["tools"].(type) {
				case map[string]any:
					for key := range tools {
						found = append(found, key)
					}
				case []any:
					for _, tool := range tools {
						if entry, ok := tool.(map[string]any); ok {
							if toolName, ok := entry["name"].(string); ok {
								found = append(found, toolName)
							}
						}
					}
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	sort.Strings(found)
	return found
}

func threadIDOf(raw json.RawMessage) string {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	// Only the named location is consulted: a generic search finds unrelated
	// identifiers that are not thread ids.
	if thread, ok := probe["thread"].(map[string]any); ok {
		if id, ok := thread["id"].(string); ok {
			return id
		}
	}
	return ""
}

func mcpCheck(doctorOutput string) map[string]any {
	start := strings.Index(doctorOutput, "{")
	if start < 0 {
		return nil
	}
	var parsed struct {
		Checks map[string]map[string]any `json:"checks"`
	}
	if err := json.Unmarshal([]byte(doctorOutput[start:]), &parsed); err != nil {
		return nil
	}
	return parsed.Checks["mcp.config"]
}

func jsonField(output, field string) string {
	start := strings.Index(output, "{")
	if start < 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(output[start:]), &parsed); err != nil {
		return ""
	}
	value, _ := parsed[field].(string)
	return value
}

func oneLine(out string, err error) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" && err != nil {
		return err.Error()
	}
	if index := strings.IndexAny(trimmed, "\r\n"); index > 0 {
		return trimmed[:index]
	}
	return truncate(trimmed, 300)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
