package runner

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// fakeRuntime records the invocations, so the tests assert on the command line
// that would actually reach the runtime. Every guardrail in docs/security.md is
// a flag, and a flag that is not there is a guardrail that is not there.
type fakeRuntime struct {
	calls    [][]string
	stdout   string
	stderr   string
	exitCode int
	err      error
	// block holds the run until released, so the timeout path is exercised
	// without waiting out a real one.
	block chan struct{}
}

func (f *fakeRuntime) Run(ctx context.Context, _ string, args ...string) (string, string, int, error) {
	f.calls = append(f.calls, args)
	if len(args) > 0 && args[0] == "rm" {
		return "", "", 0, nil
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return "", "", -1, ctx.Err()
		}
	}
	return f.stdout, f.stderr, f.exitCode, f.err
}

func (f *fakeRuntime) runArgs(t *testing.T) []string {
	t.Helper()
	for _, call := range f.calls {
		if len(call) > 0 && call[0] == "run" {
			return call
		}
	}
	t.Fatalf("Podman run was never invoked: %v", f.calls)
	return nil
}

func flagValue(args []string, flag string) (string, bool) {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func newRunner(runtime *fakeRuntime) *Runner {
	at := time.Unix(1700000000, 0).UTC()
	return &Runner{
		Runtime: runtime,
		Now:     func() time.Time { return at },
		Name:    func(projectID string, _ time.Time) string { return "lctk-" + projectID + "-run-test" },
	}
}

func request() Request {
	// The runner accepts the authoritative native host path, so the fixture must
	// use the syntax of the host executing the test before HostPath translates it
	// into the path visible to LCTK's managed runtime.
	workspace := "/work/alpha"
	if goruntime.GOOS == "windows" {
		workspace = `D:\work\alpha`
	}
	return Request{
		ProjectID: "alpha-aaaaaaaa",
		Workspace: workspace,
		Image:     "golang:1.25",
		Command:   "go test ./...",
	}
}

// Each of these is a line in docs/security.md. Asserting them together is
// deliberate: they are one guarantee, and any one missing undoes it.
func TestEveryGuardrailReachesTheRuntime(t *testing.T) {
	runtime := &fakeRuntime{}
	if _, err := newRunner(runtime).Run(context.Background(), request()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args := runtime.runArgs(t)

	// Windows projects cross the WSL automount boundary; Unix projects already
	// use the native absolute path exposed to the managed runtime.
	wantVolume := `/work/alpha:/workspace`
	if goruntime.GOOS == "windows" {
		wantVolume = `/mnt/d/work/alpha:/workspace`
	}
	if got, _ := flagValue(args, "--volume"); got != wantVolume {
		t.Errorf("--volume = %q, want the one project root", got)
	}
	if got, _ := flagValue(args, "--workdir"); got != "/workspace" {
		t.Errorf("--workdir = %q, want a fixed working directory", got)
	}
	if got, _ := flagValue(args, "--network"); got != "none" {
		t.Errorf("--network = %q, want none by default", got)
	}
	if got, _ := flagValue(args, "--pids-limit"); got != "512" {
		t.Errorf("--pids-limit = %q, want the default process cap", got)
	}
	if got, _ := flagValue(args, "--memory"); got != "" && got != "2048m" {
		t.Errorf("--memory = %q", got)
	}

	// One mount and no others.
	mounts := 0
	for _, arg := range args {
		if arg == "--volume" || arg == "--mount" {
			mounts++
		}
	}
	if mounts != 1 {
		t.Errorf("the container gets %d mounts, want exactly the project root", mounts)
	}

	// No container-runtime socket, ever.
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "docker.sock") || strings.Contains(joined, "/var/run/docker") {
		t.Errorf("the container-runtime socket reached the container: %s", joined)
	}
	if hasFlag(args, "--privileged") || hasFlag(args, "--cap-add") {
		t.Errorf("the container was given extra capabilities: %s", joined)
	}
}

func TestTheProjectResourceBudgetApplies(t *testing.T) {
	runtime := &fakeRuntime{}
	req := request()
	req.Limits = Limits{CPUs: 1, MemoryMB: 512, PIDs: 64}
	if _, err := newRunner(runtime).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	args := runtime.runArgs(t)

	for flag, want := range map[string]string{"--cpus": "1", "--memory": "512m", "--pids-limit": "64"} {
		if got, _ := flagValue(args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
}

// "full" is the project's own network rather than the default bridge, so a
// command with egress still cannot reach another project's services.
func TestFullNetworkUsesTheProjectsOwnNetwork(t *testing.T) {
	runtime := &fakeRuntime{}
	req := request()
	req.Network = "full"
	req.NetworkName = "lctk-alpha-aaaaaaaa-net"
	if _, err := newRunner(runtime).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got, _ := flagValue(runtime.runArgs(t), "--network"); got != "lctk-alpha-aaaaaaaa-net" {
		t.Fatalf("--network = %q, want the project's own network", got)
	}
}

// A policy value nobody recognises must not become "no restriction".
func TestAnUnrecognisedNetworkPolicyFallsBackToNone(t *testing.T) {
	runtime := &fakeRuntime{}
	req := request()
	req.Network = "host"
	req.NetworkName = "lctk-alpha-net"
	if _, err := newRunner(runtime).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got, _ := flagValue(runtime.runArgs(t), "--network"); got != "none" {
		t.Fatalf("--network = %q, want none for an unrecognised policy", got)
	}
}

// A failing test is the ordinary case. Reporting it as an error would leave the
// caller unable to tell it from the runtime being down.
func TestANonZeroExitIsAResultRatherThanAnError(t *testing.T) {
	runtime := &fakeRuntime{exitCode: 2, stdout: "FAIL\n", stderr: "one test failed\n"}
	result, err := newRunner(runtime).Run(context.Background(), request())
	if err != nil {
		t.Fatalf("a failing command produced an error: %v", err)
	}
	if result.ExitCode != 2 || result.Stdout != "FAIL\n" || result.Stderr != "one test failed\n" {
		t.Fatalf("result = %+v", result)
	}
	if result.TimedOut {
		t.Error("a command that exited was reported as timed out")
	}
}

// A test that never finishes and a test that fails call for different things.
func TestATimeoutIsDistinguishableFromAFailure(t *testing.T) {
	runtime := &fakeRuntime{block: make(chan struct{})}
	req := request()
	req.Limits = Limits{Timeout: 50 * time.Millisecond}

	result, err := newRunner(runtime).Run(context.Background(), req)
	if err != nil {
		t.Fatalf("a timeout produced an error rather than a result: %v", err)
	}
	if !result.TimedOut {
		t.Fatal("the run was not reported as timed out")
	}
	if result.ExitCode >= 0 {
		t.Errorf("exit code = %d, want it not read as the command's own", result.ExitCode)
	}
}

// Process-tree cleanup is removing the container, and it has to happen whatever
// the outcome — especially after a timeout, when something is still running.
func TestTheContainerIsAlwaysRemoved(t *testing.T) {
	for _, name := range []string{"success", "failure", "timeout"} {
		t.Run(name, func(t *testing.T) {
			runtime := &fakeRuntime{}
			req := request()
			switch name {
			case "failure":
				runtime.exitCode = 1
			case "timeout":
				runtime.block = make(chan struct{})
				req.Limits = Limits{Timeout: 50 * time.Millisecond}
			}
			if _, err := newRunner(runtime).Run(context.Background(), req); err != nil {
				t.Fatal(err)
			}

			removed := false
			for _, call := range runtime.calls {
				if len(call) > 0 && call[0] == "rm" && hasFlag(call, "--force") {
					removed = true
				}
			}
			if !removed {
				t.Fatalf("the container was not removed: %v", runtime.calls)
			}
		})
	}
}

// A build that fails says why at the end, so the tail is what a truncated log
// must keep.
func TestOutputIsBoundedAndKeepsTheTail(t *testing.T) {
	runtime := &fakeRuntime{stdout: strings.Repeat("a", 50) + "THE FAILURE"}
	req := request()
	req.Limits = Limits{MaxOutputBytes: 20}

	result, err := newRunner(runtime).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.StdoutTruncated {
		t.Fatal("a stream over the limit was not reported as truncated")
	}
	if len(result.Stdout) != 20 {
		t.Fatalf("stdout is %d bytes, want 20", len(result.Stdout))
	}
	if !strings.HasSuffix(result.Stdout, "THE FAILURE") {
		t.Fatalf("the tail was discarded: %q", result.Stdout)
	}
}

func TestTheRuntimeAndImageFailuresAreDistinguishable(t *testing.T) {
	down := &fakeRuntime{exitCode: -1, err: errors.New("exec failed"),
		stderr: "Unable to connect to Podman. Is the Podman machine running?"}
	if _, err := newRunner(down).Run(context.Background(), request()); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Errorf("a stopped runtime gave %v, want %v", err, ErrRuntimeUnavailable)
	}

	missing := &fakeRuntime{exitCode: -1, err: errors.New("exec failed"),
		stderr: "Unable to find image 'golang:1.25' locally"}
	if _, err := newRunner(missing).Run(context.Background(), request()); !errors.Is(err, ErrImageMissing) {
		t.Errorf("a missing image gave %v, want %v", err, ErrImageMissing)
	}
}

// The result has to carry what actually ran, so it can be read later without
// reconstructing the policy that produced it.
func TestTheResultRecordsWhatRan(t *testing.T) {
	runtime := &fakeRuntime{}
	req := request()
	req.Network = "full"
	req.NetworkName = "lctk-alpha-net"

	result, err := newRunner(runtime).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Image != "golang:1.25" || result.Command != "go test ./..." || result.Network != "full" {
		t.Fatalf("result = %+v", result)
	}
}

// The command is a shell line because that is what a developer would type, and
// that is only safe because a human approved this exact text.
func TestTheCommandIsPassedToAShellAfterTheImage(t *testing.T) {
	runtime := &fakeRuntime{}
	if _, err := newRunner(runtime).Run(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	args := runtime.runArgs(t)

	if args[len(args)-1] != "go test ./..." || args[len(args)-2] != "-c" {
		t.Fatalf("the command is not the shell's argument: %v", args[len(args)-3:])
	}
	if args[len(args)-3] != "golang:1.25" {
		t.Fatalf("the image is not immediately before the shell arguments: %v", args[len(args)-4:])
	}
	if got, _ := flagValue(args, "--entrypoint"); got != "/bin/sh" {
		t.Fatalf("--entrypoint = %q", got)
	}
}
