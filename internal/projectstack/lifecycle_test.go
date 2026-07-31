package projectstack

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRunner answers container-runtime commands from a script, so lifecycle logic
// is testable without Docker.
type fakeRunner struct {
	// responses maps a matched substring of the joined arguments to a reply.
	responses []fakeResponse
	calls     [][]string
}

type fakeResponse struct {
	match  string
	stdout string
	stderr string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, string, error) {
	f.calls = append(f.calls, args)
	joined := strings.Join(args, " ")
	for _, response := range f.responses {
		if strings.Contains(joined, response.match) {
			return response.stdout, response.stderr, response.err
		}
	}
	return "", "", nil
}

// callWith returns the first recorded call containing every fragment.
func (f *fakeRunner) callWith(fragments ...string) []string {
	for _, call := range f.calls {
		joined := strings.Join(call, " ")
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(joined, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return call
		}
	}
	return nil
}

func healthyRunner() *fakeRunner {
	return &fakeRunner{responses: []fakeResponse{
		{match: "version", stdout: "29.5.3\n"},
		{match: "image inspect", stdout: "sha256:abc\n"},
		{match: "inspect", stdout: "running healthy\n"},
	}}
}

func TestStateRetryableOnlyWhileStarting(t *testing.T) {
	if !StateStarting.Retryable() {
		t.Error("starting should be retryable so a caller waits rather than failing")
	}
	for _, state := range []State{StateStopped, StateRunning, StateError, StateUnknown} {
		if state.Retryable() {
			t.Errorf("%s should not be retryable", state)
		}
	}
}

func TestRuntimeUnavailableIsDistinguishable(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{match: "version", stderr: "cannot connect to the Docker daemon", err: errors.New("exit status 1")},
	}}
	manager := NewManagerWithRunner(runner)

	err := manager.RuntimeAvailable(context.Background())
	if !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("got %v, want ErrRuntimeUnavailable", err)
	}
	// A caller must be able to tell "Docker is closed" from "this project broke".
	if errors.Is(err, ErrImageMissing) || errors.Is(err, ErrInvalidProject) {
		t.Error("runtime failure was conflated with a project failure")
	}

	if _, err := manager.Start(context.Background(), testProject("alpha-abcd1234", absPath("work", "alpha")), 0); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Errorf("Start: got %v, want ErrRuntimeUnavailable", err)
	}
}

func TestMissingImageIsDistinguishable(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{match: "version", stdout: "29.5.3\n"},
		{match: "image inspect", stderr: "No such image", err: errors.New("exit status 1")},
	}}
	manager := NewManagerWithRunner(runner)

	_, err := manager.Start(context.Background(), testProject("alpha-abcd1234", absPath("work", "alpha")), 0)
	if !errors.Is(err, ErrImageMissing) {
		t.Fatalf("got %v, want ErrImageMissing", err)
	}
	if !strings.Contains(err.Error(), ImageRepository) {
		t.Errorf("error should name the image: %v", err)
	}
}

func TestInspectMapsRuntimeStateToLifecycleState(t *testing.T) {
	cases := []struct {
		name      string
		stdout    string
		stderr    string
		err       error
		want      State
		wantError bool
	}{
		{name: "healthy", stdout: "running healthy\n", want: StateRunning},
		{name: "health starting", stdout: "running starting\n", want: StateStarting},
		{name: "no healthcheck", stdout: "running\n", want: StateStarting},
		{name: "unhealthy", stdout: "running unhealthy\n", want: StateError},
		{name: "created", stdout: "created\n", want: StateStarting},
		{name: "restarting", stdout: "restarting\n", want: StateStarting},
		{name: "exited", stdout: "exited\n", want: StateStopped},
		{name: "dead", stdout: "dead\n", want: StateStopped},
		{name: "paused", stdout: "paused\n", want: StateStopped},
		{
			name:   "absent container is simply stopped",
			stderr: "Error: No such object: lctk-alpha-abcd1234-code-intel",
			err:    errors.New("exit status 1"),
			want:   StateStopped,
		},
		{
			name:      "unexplained failure is unknown",
			stderr:    "something went badly wrong",
			err:       errors.New("exit status 1"),
			want:      StateUnknown,
			wantError: true,
		},
		{name: "unrecognized state", stdout: "teleporting\n", want: StateUnknown},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{responses: []fakeResponse{
				{match: "version", stdout: "29.5.3\n"},
				{match: "inspect", stdout: testCase.stdout, stderr: testCase.stderr, err: testCase.err},
			}}
			manager := NewManagerWithRunner(runner)

			status, err := manager.Status(context.Background(), testProject("alpha-abcd1234", absPath("work", "alpha")))
			if testCase.wantError && err == nil {
				t.Error("expected an error")
			}
			if !testCase.wantError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if status.State != testCase.want {
				t.Errorf("state = %q, want %q", status.State, testCase.want)
			}
			// Every status must carry the resource names so a caller can act on
			// it without a second lookup.
			if status.Image == "" || status.Network == "" || status.Volume == "" {
				t.Errorf("status is missing resource names: %+v", status)
			}
			if status.State != StateRunning && status.State != StateStopped && status.Detail == "" {
				t.Errorf("state %q has no explanation", status.State)
			}
		})
	}
}

func TestStartWritesComposeAndPinsTheStack(t *testing.T) {
	isolate(t)
	runner := healthyRunner()
	manager := NewManagerWithRunner(runner)
	project := testProject("alpha-abcd1234", absPath("work", "alpha"))

	status, err := manager.Start(context.Background(), project, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateRunning {
		t.Errorf("state = %q, want running", status.State)
	}

	// The compose file must have been generated.
	composePath, err := ComposeFilePath(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	up := runner.callWith("compose", "up", "--detach")
	if up == nil {
		t.Fatalf("no compose up call was made; calls: %v", runner.calls)
	}
	joined := strings.Join(up, " ")
	// Pinning both the file and the project name means the command can never act
	// on an unrelated Compose stack.
	if !strings.Contains(joined, composePath) {
		t.Errorf("compose up is not pinned to the generated file: %v", up)
	}
	if !strings.Contains(joined, "--project-name lctk-alpha-abcd1234") {
		t.Errorf("compose up is not pinned to the project name: %v", up)
	}
}

// TestStopPreservesTheProjectVolume guards the remove-versus-purge distinction: a
// stop that deleted volumes would destroy indexes and memory.
func TestStopPreservesTheProjectVolume(t *testing.T) {
	isolate(t)
	runner := healthyRunner()
	manager := NewManagerWithRunner(runner)
	project := testProject("alpha-abcd1234", absPath("work", "alpha"))

	status, err := manager.Stop(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateStopped {
		t.Errorf("state = %q, want stopped", status.State)
	}

	down := runner.callWith("compose", "down")
	if down == nil {
		t.Fatalf("no compose down call was made; calls: %v", runner.calls)
	}
	for _, arg := range down {
		if arg == "--volumes" || arg == "-v" {
			t.Fatalf("stop asked Compose to delete volumes: %v", down)
		}
	}
	if !strings.Contains(strings.Join(down, " "), "--remove-orphans") {
		t.Errorf("stop should remove orphaned containers: %v", down)
	}
}

func TestRestartStopsThenStarts(t *testing.T) {
	isolate(t)
	runner := healthyRunner()
	manager := NewManagerWithRunner(runner)
	project := testProject("alpha-abcd1234", absPath("work", "alpha"))

	if _, err := manager.Restart(context.Background(), project, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	var order []string
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		switch {
		case strings.Contains(joined, "compose") && strings.Contains(joined, "down"):
			order = append(order, "down")
		case strings.Contains(joined, "compose") && strings.Contains(joined, "up"):
			order = append(order, "up")
		}
	}
	if len(order) != 2 || order[0] != "down" || order[1] != "up" {
		t.Errorf("restart order = %v, want down then up", order)
	}
}

func TestWaitForHealthTimesOutWithTheLastObservedState(t *testing.T) {
	isolate(t)
	runner := &fakeRunner{responses: []fakeResponse{
		{match: "version", stdout: "29.5.3\n"},
		{match: "image inspect", stdout: "sha256:abc\n"},
		// Never becomes healthy.
		{match: "inspect", stdout: "running starting\n"},
	}}
	manager := NewManagerWithRunner(runner)

	status, err := manager.Start(context.Background(), testProject("alpha-abcd1234", absPath("work", "alpha")), 400*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	// The caller must learn that it was still starting, which is retryable, not
	// that it failed outright.
	if status.State != StateStarting {
		t.Errorf("state = %q, want starting", status.State)
	}
	if !status.State.Retryable() {
		t.Error("a timeout while starting should be retryable")
	}
	if !strings.Contains(err.Error(), "did not become healthy") {
		t.Errorf("error should explain the timeout: %v", err)
	}
}

func TestWaitForHealthFailsFastOnUnhealthy(t *testing.T) {
	isolate(t)
	runner := &fakeRunner{responses: []fakeResponse{
		{match: "version", stdout: "29.5.3\n"},
		{match: "image inspect", stdout: "sha256:abc\n"},
		{match: "inspect", stdout: "running unhealthy\n"},
	}}
	manager := NewManagerWithRunner(runner)

	started := time.Now()
	status, err := manager.Start(context.Background(), testProject("alpha-abcd1234", absPath("work", "alpha")), 10*time.Second)
	if err == nil {
		t.Fatal("expected a failure")
	}
	if status.State != StateError {
		t.Errorf("state = %q, want error", status.State)
	}
	// An unhealthy container is terminal, so waiting out the budget would waste
	// the caller's time.
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("waited %s before reporting a terminal failure", elapsed)
	}
}

func TestBuildImageReportsTheFailure(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{match: "build", stderr: "failed to solve: no such file", err: errors.New("exit status 1")},
	}}
	manager := NewManagerWithRunner(runner)

	err := manager.BuildImage(context.Background(), "lctk/code-intel:test", "missing-dir")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("error should carry the runtime message: %v", err)
	}
}

func TestIsNoSuchContainer(t *testing.T) {
	if !isNoSuchContainer("", "Error: No such object: abc") {
		t.Error("no such object was not recognized")
	}
	if !isNoSuchContainer("", "no such container: abc") {
		t.Error("no such container was not recognized")
	}
	if isNoSuchContainer("", "permission denied") {
		t.Error("an unrelated error was treated as a missing container")
	}
}
