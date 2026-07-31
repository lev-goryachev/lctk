package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lev-goryachev/lctk/internal/projectstack"
)

// scriptedRuntime answers container-runtime commands from a script so that the
// command tests behave identically whether or not Docker is installed.
type scriptedRuntime struct {
	replies []scriptedReply
	calls   [][]string
}

type scriptedReply struct {
	match  string
	stdout string
	stderr string
	err    error
}

func (s *scriptedRuntime) Run(_ context.Context, args ...string) (string, string, error) {
	s.calls = append(s.calls, args)
	joined := strings.Join(args, " ")
	for _, reply := range s.replies {
		if strings.Contains(joined, reply.match) {
			return reply.stdout, reply.stderr, reply.err
		}
	}
	return "", "", nil
}

func (s *scriptedRuntime) sawCall(fragments ...string) bool {
	for _, call := range s.calls {
		joined := strings.Join(call, " ")
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(joined, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// useRuntime installs a scripted runtime for the duration of a test.
func useRuntime(t *testing.T, replies ...scriptedReply) *scriptedRuntime {
	t.Helper()
	runtime := &scriptedRuntime{replies: replies}
	previous := newStackManager
	newStackManager = func() *projectstack.Manager {
		return projectstack.NewManagerWithRunner(runtime)
	}
	t.Cleanup(func() { newStackManager = previous })
	return runtime
}

func healthyRuntime(t *testing.T) *scriptedRuntime {
	t.Helper()
	return useRuntime(t,
		scriptedReply{match: "version", stdout: "29.5.3\n"},
		scriptedReply{match: "image inspect", stdout: "sha256:abc\n"},
		scriptedReply{match: "inspect", stdout: "running healthy\n"},
	)
}

func unavailableRuntime(t *testing.T) *scriptedRuntime {
	t.Helper()
	return useRuntime(t, scriptedReply{
		match:  "version",
		stderr: "cannot connect to the Docker daemon",
		err:    errors.New("exit status 1"),
	})
}

// addProject registers a folder and returns its identifier.
func addProject(t *testing.T, name string) string {
	t.Helper()
	dir := makeProjectDir(t, t.TempDir(), name)
	stdout, _, err := project(t, "add", "--json", dir)
	if err != nil {
		t.Fatal(err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	return view.ID
}

func TestProjectStartReportsRunningState(t *testing.T) {
	isolateHome(t)
	runtime := healthyRuntime(t)
	id := addProject(t, "alpha")

	stdout, _, err := project(t, "start", "--json", "--wait", "5s", id)
	if err != nil {
		t.Fatal(err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if view.State != string(projectstack.StateRunning) {
		t.Errorf("state = %q, want running", view.State)
	}
	if view.Health != "healthy" {
		t.Errorf("health = %q", view.Health)
	}
	if view.Retryable {
		t.Error("a running project should not be marked retryable")
	}
	if view.Volume == "" || view.Network == "" {
		t.Errorf("resource names missing: %+v", view)
	}
	if !runtime.sawCall("compose", "up", "--detach") {
		t.Errorf("compose up was not invoked; calls: %v", runtime.calls)
	}
}

func TestProjectStopKeepsTheVolume(t *testing.T) {
	isolateHome(t)
	runtime := healthyRuntime(t)
	id := addProject(t, "alpha")

	stdout, _, err := project(t, "stop", "--json", id)
	if err != nil {
		t.Fatal(err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != string(projectstack.StateStopped) {
		t.Errorf("state = %q, want stopped", view.State)
	}
	if !runtime.sawCall("compose", "down") {
		t.Errorf("compose down was not invoked; calls: %v", runtime.calls)
	}
	// Deleting volumes on stop would destroy indexes and memory.
	if runtime.sawCall("compose", "down", "--volumes") {
		t.Error("stop asked Compose to delete volumes")
	}
}

func TestProjectRestartGoesDownThenUp(t *testing.T) {
	isolateHome(t)
	runtime := healthyRuntime(t)
	id := addProject(t, "alpha")

	if _, _, err := project(t, "restart", "--json", "--wait", "5s", id); err != nil {
		t.Fatal(err)
	}
	if !runtime.sawCall("compose", "down") || !runtime.sawCall("compose", "up") {
		t.Errorf("restart did not go down then up; calls: %v", runtime.calls)
	}
}

// TestProjectStartWithoutRuntimeIsDistinguishable matters for a calling agent: a
// closed Docker Desktop must not look like a broken project.
func TestProjectStartWithoutRuntimeIsDistinguishable(t *testing.T) {
	isolateHome(t)
	unavailableRuntime(t)
	id := addProject(t, "alpha")

	stdout, _, err := project(t, "start", "--json", id)
	if !errors.Is(err, projectstack.ErrRuntimeUnavailable) {
		t.Fatalf("got %v, want ErrRuntimeUnavailable", err)
	}
	// The status is still emitted so the caller can act on it.
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("no status was emitted on failure: %v\n%s", err, stdout)
	}
	if view.Detail == "" {
		t.Error("failure detail is empty")
	}
}

func TestProjectStartWithoutTheImageSaysSo(t *testing.T) {
	isolateHome(t)
	useRuntime(t,
		scriptedReply{match: "version", stdout: "29.5.3\n"},
		scriptedReply{match: "image inspect", stderr: "No such image", err: errors.New("exit status 1")},
	)
	id := addProject(t, "alpha")

	_, _, err := project(t, "start", id)
	if !errors.Is(err, projectstack.ErrImageMissing) {
		t.Fatalf("got %v, want ErrImageMissing", err)
	}
}

func TestProjectStatusIncludesRuntimeState(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	stdout, _, err := project(t, "status", "--json", id)
	if err != nil {
		t.Fatal(err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != string(projectstack.StateRunning) {
		t.Errorf("state = %q, want running", view.State)
	}

	// The human table must carry the state column too.
	stdout, _, err = project(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "STATE") || !strings.Contains(stdout, "running") {
		t.Errorf("status listing lacks runtime state:\n%s", stdout)
	}
	_ = id
}

// TestProjectStatusSurvivesAnUnavailableRuntime keeps registry information usable
// while Docker Desktop is closed.
func TestProjectStatusSurvivesAnUnavailableRuntime(t *testing.T) {
	isolateHome(t)
	unavailableRuntime(t)
	id := addProject(t, "alpha")

	stdout, _, err := project(t, "status", "--json", id)
	if err != nil {
		t.Fatalf("status must not fail when the runtime is unavailable: %v", err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != string(projectstack.StateUnknown) {
		t.Errorf("state = %q, want unknown", view.State)
	}
	if view.Detail == "" {
		t.Error("no explanation was given for the unknown state")
	}
	// The registry answer must still be present.
	if view.Path == "" || view.ID == "" {
		t.Errorf("registry information was lost: %+v", view)
	}
}

func TestProjectStatusMarksAStartingProjectRetryable(t *testing.T) {
	isolateHome(t)
	useRuntime(t,
		scriptedReply{match: "version", stdout: "29.5.3\n"},
		scriptedReply{match: "inspect", stdout: "running starting\n"},
	)
	id := addProject(t, "alpha")

	stdout, _, err := project(t, "status", "--json", id)
	if err != nil {
		t.Fatal(err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.State != string(projectstack.StateStarting) {
		t.Errorf("state = %q, want starting", view.State)
	}
	if !view.Retryable {
		t.Error("a starting project must be marked retryable so a caller waits")
	}
}

func TestProjectRemoveStopsTheStackAndKeepsData(t *testing.T) {
	isolateHome(t)
	runtime := healthyRuntime(t)
	dir := makeProjectDir(t, t.TempDir(), "alpha")
	if _, _, err := project(t, "add", dir); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := project(t, "remove", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.sawCall("compose", "down") {
		t.Errorf("remove did not release runtime resources; calls: %v", runtime.calls)
	}
	if runtime.sawCall("compose", "down", "--volumes") {
		t.Error("remove deleted the project volume implicitly")
	}
	if !strings.Contains(stdout, "separate purge") {
		t.Errorf("output does not explain that the volume was kept:\n%s", stdout)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("remove deleted the project folder: %v", err)
	}
}

// TestProjectRemoveSucceedsWithoutARuntime keeps deregistration possible while
// Docker Desktop is closed.
func TestProjectRemoveSucceedsWithoutARuntime(t *testing.T) {
	isolateHome(t)
	unavailableRuntime(t)
	dir := makeProjectDir(t, t.TempDir(), "alpha")
	if _, _, err := project(t, "add", dir); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := project(t, "remove", "alpha")
	if err != nil {
		t.Fatalf("remove must not require a runtime: %v", err)
	}
	if !strings.Contains(stdout, "runtime unavailable") {
		t.Errorf("output does not warn that the stack was not stopped:\n%s", stdout)
	}
}

func TestProjectLifecycleUsageErrors(t *testing.T) {
	isolateHome(t)
	healthyRuntime(t)
	for _, action := range []string{"start", "stop", "restart"} {
		if _, _, err := project(t, action); err == nil {
			t.Errorf("%s without a project was accepted", action)
		}
		if _, _, err := project(t, action, "missing-project"); err == nil {
			t.Errorf("%s of an unknown project was accepted", action)
		}
	}
	// stop takes no wait budget.
	if _, _, err := project(t, "stop", "--wait", "5s", "alpha"); err == nil {
		t.Error("stop accepted a --wait flag")
	}
}

func imageCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runImage(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestImageStatusReportsAvailability(t *testing.T) {
	isolateHome(t)
	useRuntime(t,
		scriptedReply{match: "version", stdout: "29.5.3\n"},
		scriptedReply{match: "image inspect", stdout: "sha256:abc\n"},
	)

	stdout, _, err := imageCommand(t, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var view imageView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Available {
		t.Errorf("image reported unavailable: %+v", view)
	}
	if !strings.HasPrefix(view.Image, projectstack.ImageRepository+":") {
		t.Errorf("image = %q", view.Image)
	}
}

func TestImageStatusExplainsAMissingImage(t *testing.T) {
	isolateHome(t)
	useRuntime(t,
		scriptedReply{match: "version", stdout: "29.5.3\n"},
		scriptedReply{match: "image inspect", stderr: "No such image", err: errors.New("exit status 1")},
	)

	stdout, _, err := imageCommand(t, "status", "--json")
	if err != nil {
		t.Fatalf("a missing image is an answer, not a failure: %v", err)
	}
	var view imageView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.Available {
		t.Error("a missing image was reported as available")
	}
	if !strings.Contains(view.Detail, "lctk image build") {
		t.Errorf("detail does not say how to fix it: %q", view.Detail)
	}
}

func TestImageBuildPassesTheContextDirectory(t *testing.T) {
	isolateHome(t)
	runtime := useRuntime(t, scriptedReply{match: "version", stdout: "29.5.3\n"})

	contextDir := t.TempDir()
	if _, _, err := imageCommand(t, "build", "--context", contextDir, "--json"); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(contextDir)
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.sawCall("build", "--tag", absolute) {
		t.Errorf("build was not invoked with the context directory; calls: %v", runtime.calls)
	}
}

func TestImageUsageErrors(t *testing.T) {
	isolateHome(t)
	if _, stderr, err := imageCommand(t); err == nil {
		t.Error("an empty image subcommand was accepted")
	} else if !strings.Contains(stderr, "lctk image build") {
		t.Errorf("usage was not printed:\n%s", stderr)
	}
	if _, _, err := imageCommand(t, "frobnicate"); err == nil {
		t.Error("an unknown image subcommand was accepted")
	}
	stdout, _, err := imageCommand(t, "help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "lctk image status") {
		t.Errorf("help output:\n%s", stdout)
	}
}

func TestImageCommandIsReachableFromRun(t *testing.T) {
	isolateHome(t)
	useRuntime(t,
		scriptedReply{match: "version", stdout: "29.5.3\n"},
		scriptedReply{match: "image inspect", stdout: "sha256:abc\n"},
	)

	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"image", "status"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), projectstack.ImageRepository) {
		t.Errorf("image is not wired into the top-level command:\n%s", stdout.String())
	}
}
