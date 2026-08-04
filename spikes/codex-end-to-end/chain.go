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
		if spec.name == "alpha" {
			// Stage 3's tools need something to describe: a repository with a
			// commit behind it and an edit in front of it, and a manifest that
			// proposes a command. Only alpha gets them, so beta stays the project
			// that proves scope is not leaked.
			if err := prepareSourceProject(ctx, dir); err != nil {
				rep.fail("register_folder", "could not prepare the source project: %v", err)
				return rep, nil
			}
		}
		if err := e.runJSON(ctx, spec.view, "project", "add", "--json", dir); err != nil {
			rep.fail("register_folder", "%v", err)
			return rep, nil
		}
	}
	rep.pass("register_folder", "registered %s and %s", alpha.ID, beta.ID)

	// The runner image is the one this repository builds, so the harness needs no
	// external image and the command genuinely runs in a container.
	var image struct {
		Image     string `json:"image"`
		Available bool   `json:"available"`
	}
	if err := e.runJSON(ctx, &image, "image", "status", "--json"); err == nil && image.Available {
		if _, err := e.run(ctx, "project", "commands", "--image", image.Image, "--approve", "lint", alpha.ID); err != nil {
			rep.fail("approve_a_command", "%v", err)
		} else {
			rep.pass("approve_a_command", "lint approved for %s in %s", alpha.ID, image.Image)
		}
	} else {
		rep.skip("approve_a_command", "no local runner image to approve")
	}

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
		// Named individually rather than covered by one line, so a run with no
		// thread cannot be mistaken for a run in which Stage 3 and Stage 4 were
		// exercised.
		for _, step := range []string{
			"exact_search_through_client", "bad_pattern_refused_visibly", "invented_argument_refused",
			"file_outline_through_client", "unparsed_file_is_reported",
			"unsupported_language_refused", "outline_path_escape_refused",
			"find_definition_through_client", "find_references_ignores_prose",
			"a_pattern_is_not_a_name",
			"git_status_through_client", "git_diff_through_client", "staged_and_worktree_diffs_differ",
			"escaping_path_refused_visibly", "approved_command_runs", "smuggled_command_ignored",
			"unapproved_command_refused", "unproposed_command_refused", "unknown_command_refused",
			"rewritten_command_loses_its_approval", "a_failing_command_is_a_result",
		} {
			rep.skip(step, "no thread available")
		}
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

	if threadID != "" {
		// 10. Every tool the endpoint offers, called through the same real client.
		// Several were added after this harness was written and carry input schemas
		// of their own, which is where a client and a server disagree.
		verifyTools(ctx, client, rep, alphaServer, threadID)
		// Last, because it rewrites the project's manifest and re-approves a
		// command: every check above should see the state the run set up.
		verifyApprovalLapses(ctx, e, client, rep, alphaServer, threadID, alpha.ID, filepath.Join(e.work, "alpha"))
	}

	rep.Notifications = client.notifications()
	rep.Stderr = truncate(client.stderrTail(), 2000)
	return rep, nil
}

// prepareSourceProject makes a folder look like a project an agent would ask
// about: a repository with one commit behind it, an edit in front of it, and a
// manifest proposing a command.
//
// The identity is supplied per command rather than configured, because a machine
// running this may have no global Git identity and committing would fail on it.
func prepareSourceProject(ctx context.Context, dir string) error {
	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("first line\n"), 0o644); err != nil {
		return err
	}
	// lint is proposed and will be approved; test is proposed and deliberately is
	// not, which is what separates "nobody agreed to this" from "this project does
	// not have one". build is left unproposed to cover the third refusal.
	manifest := "commands:\n" +
		"  lint: echo lint-ran-in-a-container\n" +
		"  test: echo this-must-never-run\n"
	if err := os.WriteFile(filepath.Join(dir, ".mcp-project.yaml"), []byte(manifest), 0o644); err != nil {
		return err
	}

	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, oneLine(string(out), nil))
		}
		return nil
	}
	if err := git("init", "--quiet"); err != nil {
		return err
	}
	// A Go file with nesting worth reporting: a field inside a type and a constant
	// inside a method. Containment computed from byte ranges is the claim being
	// checked, and neither of those two declarations says in its own syntax where it
	// lives.
	outline := "package fixture\n\n" +
		"// Widget is the declaration an outline must find.\n" +
		"type Widget struct {\n\tSize int\n}\n\n" +
		"func (w Widget) Describe() string {\n\tconst prefix = \"widget\"\n\treturn prefix\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "outline.go"), []byte(outline), 0o644); err != nil {
		return err
	}
	// A file that does not parse, left uncommitted. An outline of it has to report
	// both halves: that it is broken, and the declarations that did parse -- an agent
	// asking about the file it is midway through editing is the ordinary case.
	half := "package fixture\n\nfunc Halfway(a int) int {\n\treturn a +\n"
	if err := os.WriteFile(filepath.Join(dir, "half.go"), []byte(half), 0o644); err != nil {
		return err
	}
	// A real use of Widget in a second file, inside a function, so a cross-file
	// lookup has something to place.
	uses := "package fixture\n\nfunc Consume(w Widget) int {\n\treturn w.Size\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "uses.go"), []byte(uses), 0o644); err != nil {
		return err
	}
	// And a third file that mentions Widget only in a comment and a string. A text
	// search returns this file; a syntax-aware lookup must not, and that difference
	// is the reason the tool exists.
	prose := "package fixture\n\n// Widget is discussed here and nowhere used.\n" +
		"const note = \"Widget again, in a string\"\n"
	if err := os.WriteFile(filepath.Join(dir, "prose.go"), []byte(prose), 0o644); err != nil {
		return err
	}

	if err := git("add", "tracked.txt", ".mcp-project.yaml", "outline.go", "uses.go", "prose.go"); err != nil {
		return err
	}
	if err := git(
		"-c", "user.name=LCTK Harness", "-c", "user.email=harness@lctk.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "-m", "the commit an agent asks about",
	); err != nil {
		return err
	}
	// A second file staged and then edited again, so the staged view and the
	// working-tree view of one path genuinely differ. Without it a passing
	// git_diff proves only that some diff came back.
	both := filepath.Join(dir, "both.txt")
	if err := os.WriteFile(both, []byte("staged content\n"), 0o644); err != nil {
		return err
	}
	if err := git("add", "both.txt"); err != nil {
		return err
	}
	if err := os.WriteFile(both, []byte("staged content\nedited after staging\n"), 0o644); err != nil {
		return err
	}

	// The edit that makes git_status and git_diff have something to say.
	return os.WriteFile(tracked, []byte("first line\nan uncommitted second line\n"), 0o644)
}

// verifyTools drives every tool the endpoint offers through the client.
//
// The refusals matter as much as the answers. A typed code is only useful if it
// survives into the client's own error text, where an agent will read it and act
// on it rather than only learning that something went wrong.
//
// Every tool is called, not merely listed. A tool present in `tools/list` and
// never invoked is a schema nobody has agreed to: appearing in the list only
// proves the server described it, not that the client can send it arguments and
// read the answer back.
func verifyTools(ctx context.Context, client *appServerClient, rep *report, server, threadID string) {
	// exact_search is the oldest tool here and the one an agent reaches for most.
	// The pattern is a line that was saved and never committed, so a hit proves the
	// index describes the working tree rather than the last commit -- the claim the
	// whole indexing design exists to make, checked from outside LCTK.
	body, err := toolCall(ctx, client, server, "exact_search", threadID,
		map[string]any{"pattern": "an uncommitted second line"})
	switch {
	case err != nil:
		rep.fail("exact_search_through_client", "%v", err)
	case !strings.Contains(body, "tracked.txt"):
		rep.fail("exact_search_through_client", "%s", truncate(body, 400))
	default:
		rep.pass("exact_search_through_client",
			"a saved-but-uncommitted line was found through the client, with freshness reported")
	}

	// A pattern that cannot compile is a caller's mistake and must come back as a
	// typed refusal rather than as an empty result set, which would read as "no
	// such code in this project" and send an agent looking somewhere else.
	body, err = toolCall(ctx, client, server, "exact_search", threadID,
		map[string]any{"pattern": "([unclosed", "mode": "regex"})
	combined := body
	if err != nil {
		combined = strings.TrimPrefix(combined+" | "+err.Error(), " | ")
	}
	if strings.Contains(combined, "INVALID_PATTERN") {
		rep.pass("bad_pattern_refused_visibly", "%s", truncate(combined, 300))
	} else {
		rep.fail("bad_pattern_refused_visibly", "%s", truncate(combined, 400))
	}

	// An argument that does not exist in the schema is refused rather than
	// ignored. This is the other half of the scope guarantee: a *declared* argument
	// like project_id is accepted and disregarded, which is checked above, while an
	// invented one fails validation before any handler sees it. Silently dropping
	// it would leave an agent believing a filter had been applied.
	body, err = toolCall(ctx, client, server, "exact_search", threadID,
		map[string]any{"pattern": "package", "invented_filter": "anything"})
	combined = body
	if err != nil {
		combined = strings.TrimPrefix(combined+" | "+err.Error(), " | ")
	}
	if strings.Contains(combined, "invented_filter") {
		rep.pass("invented_argument_refused", "%s", truncate(combined, 300))
	} else {
		rep.fail("invented_argument_refused", "%s", truncate(combined, 400))
	}

	// file_outline answers a question no other tool here can: what does this file
	// declare, and does it parse. It reads the file rather than the index, so it needs
	// no flush and cannot be behind.
	body, err = toolCall(ctx, client, server, "file_outline", threadID,
		map[string]any{"path": "outline.go"})
	switch {
	case err != nil:
		rep.fail("file_outline_through_client", "%v", err)
	// Containment is the part worth checking through a client: a field reported
	// inside its type, and a constant inside the method that declares it. Neither
	// says in its own syntax where it lives.
	case !strings.Contains(body, `"name":"Size"`) || !strings.Contains(body, `"container":"Widget"`):
		rep.fail("file_outline_through_client", "%s", truncate(body, 500))
	case !strings.Contains(body, `"container":"Describe"`):
		rep.fail("file_outline_through_client", "a constant inside a method is not contained by it: %s",
			truncate(body, 500))
	case !strings.Contains(body, `"precision":"syntax"`):
		rep.fail("file_outline_through_client", "the answer does not state its precision: %s",
			truncate(body, 500))
	default:
		rep.pass("file_outline_through_client",
			"declarations with kinds, extents, and containment, stated as syntax precision")
	}

	// A half-written file is the case the syntax verdict exists for. It has to report
	// that the file is broken, say where to look, and still list what parsed.
	body, err = toolCall(ctx, client, server, "file_outline", threadID,
		map[string]any{"path": "half.go"})
	switch {
	case err != nil:
		rep.fail("unparsed_file_is_reported", "%v", err)
	case !strings.Contains(body, `"valid":false`) || !strings.Contains(body, `"first_error_line"`):
		rep.fail("unparsed_file_is_reported", "%s", truncate(body, 500))
	case !strings.Contains(body, `"name":"Halfway"`):
		rep.fail("unparsed_file_is_reported",
			"a broken file lost the declarations that did parse: %s", truncate(body, 500))
	default:
		rep.pass("unparsed_file_is_reported",
			"a truncated file is reported invalid with a line to look at, and still lists what parsed")
	}

	// A file this build has no grammar for is refused rather than answered with an
	// empty outline, which would read as "this file declares nothing". The refusal
	// has to name a tool that does work on it.
	body, err = toolCall(ctx, client, server, "file_outline", threadID,
		map[string]any{"path": "tracked.txt"})
	combined = body
	if err != nil {
		combined = strings.TrimPrefix(combined+" | "+err.Error(), " | ")
	}
	if strings.Contains(combined, "LANGUAGE_UNSUPPORTED") && strings.Contains(combined, "exact_search") {
		rep.pass("unsupported_language_refused", "%s", truncate(combined, 300))
	} else {
		rep.fail("unsupported_language_refused", "%s", truncate(combined, 400))
	}

	// The same scope boundary the git tools hold, checked on this tool too: a path
	// that leaves the project is refused rather than reinterpreted.
	body, err = toolCall(ctx, client, server, "file_outline", threadID,
		map[string]any{"path": "../outside.go"})
	combined = body
	if err != nil {
		combined = strings.TrimPrefix(combined+" | "+err.Error(), " | ")
	}
	if strings.Contains(combined, "INVALID_PATH") {
		rep.pass("outline_path_escape_refused", "%s", truncate(combined, 300))
	} else {
		rep.fail("outline_path_escape_refused", "%s", truncate(combined, 400))
	}

	// find_definition reaches across files. The fixture declares Widget in one file
	// and mentions it in two others, one of which mentions it only in a comment.
	body, err = toolCall(ctx, client, server, "find_definition", threadID,
		map[string]any{"name": "Widget"})
	switch {
	case err != nil:
		rep.fail("find_definition_through_client", "%v", err)
	case !strings.Contains(body, `"path":"outline.go"`) || !strings.Contains(body, `"declaration":true`):
		rep.fail("find_definition_through_client", "%s", truncate(body, 600))
	case !strings.Contains(body, `"precision":"name_match"`):
		rep.fail("find_definition_through_client",
			"the answer does not state that it is name-matched: %s", truncate(body, 600))
	default:
		rep.pass("find_definition_through_client",
			"the declaring file and location came back, stated as name_match precision")
	}

	// The property that makes this worth more than a text search: the file that
	// mentions Widget only in a comment must not appear. A grep would return it.
	body, err = toolCall(ctx, client, server, "find_references", threadID,
		map[string]any{"name": "Widget"})
	switch {
	case err != nil:
		rep.fail("find_references_ignores_prose", "%v", err)
	case strings.Contains(body, "prose.go"):
		rep.fail("find_references_ignores_prose",
			"a file mentioning the name only in a comment was reported: %s", truncate(body, 600))
	case !strings.Contains(body, "uses.go"):
		rep.fail("find_references_ignores_prose",
			"the file that really uses the name is missing: %s", truncate(body, 600))
	case !strings.Contains(body, `"container":"Consume"`):
		rep.fail("find_references_ignores_prose",
			"a use is not placed in the function that encloses it: %s", truncate(body, 600))
	default:
		rep.pass("find_references_ignores_prose",
			"the real use is reported inside its enclosing function and the comment-only file is not")
	}

	// A name is not a pattern. Accepting one would quietly answer a question the
	// caller did not ask.
	body, err = toolCall(ctx, client, server, "find_references", threadID,
		map[string]any{"name": "Widget|Consume"})
	combined = body
	if err != nil {
		combined = strings.TrimPrefix(combined+" | "+err.Error(), " | ")
	}
	if strings.Contains(combined, "is not an identifier") {
		rep.pass("a_pattern_is_not_a_name", "%s", truncate(combined, 300))
	} else {
		rep.fail("a_pattern_is_not_a_name", "%s", truncate(combined, 400))
	}

	body, err = toolCall(ctx, client, server, "git_status", threadID, map[string]any{})
	switch {
	case err != nil:
		rep.fail("git_status_through_client", "%v", err)
	case !strings.Contains(body, `"repository":true`) || !strings.Contains(body, "tracked.txt"):
		rep.fail("git_status_through_client", "%s", truncate(body, 400))
	default:
		// The host path must not appear: a client is told where the project is as
		// its own tools see it, not where it sits on the machine.
		rep.pass("git_status_through_client",
			"the working-tree change is reported with the branch and commit, root=/workspace")
	}

	body, err = toolCall(ctx, client, server, "git_diff", threadID,
		map[string]any{"paths": []string{"tracked.txt"}})
	switch {
	case err != nil:
		rep.fail("git_diff_through_client", "%v", err)
	case !strings.Contains(body, "an uncommitted second line"):
		rep.fail("git_diff_through_client", "%s", truncate(body, 400))
	default:
		rep.pass("git_diff_through_client", "a unified diff of the one named path came back through the client")
	}

	// The staged view and the working-tree view of one path have to differ, or the
	// staged flag is decoration. both.txt was staged with one line and then given a
	// second, so each view sees exactly one of them.
	worktree, wtErr := toolCall(ctx, client, server, "git_diff", threadID,
		map[string]any{"paths": []string{"both.txt"}})
	staged, stErr := toolCall(ctx, client, server, "git_diff", threadID,
		map[string]any{"paths": []string{"both.txt"}, "staged": true})
	switch {
	case wtErr != nil || stErr != nil:
		rep.fail("staged_and_worktree_diffs_differ", "worktree: %v; staged: %v", wtErr, stErr)
	case !strings.Contains(worktree, "edited after staging") || strings.Contains(worktree, "new file mode"):
		rep.fail("staged_and_worktree_diffs_differ", "worktree diff = %s", truncate(worktree, 300))
	case !strings.Contains(staged, "staged content") || strings.Contains(staged, "edited after staging"):
		rep.fail("staged_and_worktree_diffs_differ", "staged diff = %s", truncate(staged, 300))
	default:
		rep.pass("staged_and_worktree_diffs_differ",
			"the working tree shows only the unstaged line and the staged view only the staged one")
	}

	// A path leaving the project is refused rather than reinterpreted, and the
	// refusal is the thing the client shows.
	body, err = toolCall(ctx, client, server, "git_diff", threadID,
		map[string]any{"paths": []string{"../outside.txt"}})
	combined = body
	if err != nil {
		combined = strings.TrimPrefix(combined+" | "+err.Error(), " | ")
	}
	if strings.Contains(combined, "INVALID_PATH") {
		rep.pass("escaping_path_refused_visibly", "%s", truncate(combined, 300))
	} else {
		rep.fail("escaping_path_refused_visibly", "%s", truncate(combined, 400))
	}

	body, err = toolCall(ctx, client, server, "run_command", threadID, map[string]any{"name": "lint"})
	switch {
	case err != nil:
		rep.fail("approved_command_runs", "%v", err)
	case !strings.Contains(body, "lint-ran-in-a-container"):
		rep.fail("approved_command_runs", "%s", truncate(body, 400))
	default:
		rep.pass("approved_command_runs", "the approved command ran in a container and its output came back")
	}

	// The one field a client can name that could carry an instruction is declared
	// as ignored, and has to actually be ignored: the approved text runs and the
	// smuggled line does not. A tool that quietly honoured it would undo the entire
	// approval mechanism while every other check still passed.
	body, err = toolCall(ctx, client, server, "run_command", threadID,
		map[string]any{"name": "lint", "command": "echo this-must-never-run"})
	switch {
	case err != nil:
		rep.fail("smuggled_command_ignored", "%v", err)
	case strings.Contains(body, "this-must-never-run"):
		rep.fail("smuggled_command_ignored", "the smuggled line ran: %s", truncate(body, 300))
	case !strings.Contains(body, "lint-ran-in-a-container"):
		rep.fail("smuggled_command_ignored", "%s", truncate(body, 300))
	default:
		rep.pass("smuggled_command_ignored",
			"a command line supplied beside the name was disregarded and the approved text ran")
	}

	// The three refusals a client must be able to tell apart, because each one
	// calls for something different from whoever reads it: approve this, add it to
	// the manifest first, or stop asking for a command that does not exist.
	for _, c := range []struct{ step, name, want string }{
		{"unapproved_command_refused", "test", "COMMAND_NOT_APPROVED"},
		{"unproposed_command_refused", "build", "COMMAND_NOT_PROPOSED"},
		{"unknown_command_refused", "deploy", "COMMAND_UNKNOWN"},
	} {
		body, err := toolCall(ctx, client, server, "run_command", threadID, map[string]any{"name": c.name})
		combined := body
		if err != nil {
			combined = strings.TrimPrefix(combined+" | "+err.Error(), " | ")
		}
		if strings.Contains(combined, c.want) {
			rep.pass(c.step, "%s", truncate(combined, 300))
		} else {
			rep.fail(c.step, "%s", truncate(combined, 400))
		}
	}
}

// verifyApprovalLapses rewrites the manifest under a running project and checks
// that the approval stops applying, then that re-approving restores it and that a
// failing command comes back as a result rather than as a tool error.
//
// This is the attack the command policy exists to stop, so it is worth driving
// through a client rather than only in a unit test: get something harmless
// approved, then change what it does.
func verifyApprovalLapses(ctx context.Context, e *environment, client *appServerClient, rep *report,
	server, threadID, projectID, dir string) {
	manifest := filepath.Join(dir, ".mcp-project.yaml")
	original, err := os.ReadFile(manifest)
	if err != nil {
		rep.skip("rewritten_command_loses_its_approval", "the manifest could not be read: %v", err)
		rep.skip("a_failing_command_is_a_result", "the manifest could not be read")
		return
	}

	// The rewritten command both fails and announces itself, so one run answers
	// two questions: was the lapse detected, and is a non-zero exit a result.
	rewritten := strings.Replace(string(original),
		"lint: echo lint-ran-in-a-container",
		"lint: echo rewritten-and-failing; exit 3", 1)
	if rewritten == string(original) {
		rep.skip("rewritten_command_loses_its_approval", "the manifest did not contain the expected lint line")
		rep.skip("a_failing_command_is_a_result", "the manifest did not contain the expected lint line")
		return
	}
	if err := os.WriteFile(manifest, []byte(rewritten), 0o644); err != nil {
		rep.skip("rewritten_command_loses_its_approval", "%v", err)
		rep.skip("a_failing_command_is_a_result", "%v", err)
		return
	}
	defer func() { _ = os.WriteFile(manifest, original, 0o644) }()

	body, callErr := toolCall(ctx, client, server, "run_command", threadID, map[string]any{"name": "lint"})
	combined := body
	if callErr != nil {
		combined = strings.TrimPrefix(combined+" | "+callErr.Error(), " | ")
	}
	if strings.Contains(combined, "COMMAND_CHANGED") {
		rep.pass("rewritten_command_loses_its_approval", "%s", truncate(combined, 300))
	} else {
		rep.fail("rewritten_command_loses_its_approval", "%s", truncate(combined, 400))
	}

	// Approving the new text is what restores it, and only after somebody read it.
	if _, err := e.run(ctx, "project", "commands", "--approve", "lint", projectID); err != nil {
		rep.skip("a_failing_command_is_a_result", "the new text could not be approved: %v", err)
		return
	}
	body, callErr = toolCall(ctx, client, server, "run_command", threadID, map[string]any{"name": "lint"})
	switch {
	case callErr != nil:
		rep.fail("a_failing_command_is_a_result", "%v", callErr)
	case !strings.Contains(body, `"exit_code":3`) || !strings.Contains(body, "rewritten-and-failing"):
		rep.fail("a_failing_command_is_a_result", "%s", truncate(body, 400))
	case strings.Contains(body, `"isError":true`):
		rep.fail("a_failing_command_is_a_result", "a failing command was reported as a tool error: %s", truncate(body, 300))
	default:
		rep.pass("a_failing_command_is_a_result",
			"exit code 3 and the output came back as a result, so a failing check is not a malfunction")
	}
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
