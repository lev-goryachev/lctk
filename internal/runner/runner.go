// Package runner executes a project's approved commands inside a container.
//
// One container per run, created and destroyed around the command. That is not
// an implementation detail: it is how most of the guardrails in docs/security.md
// are met at all. Process-tree cleanup is removing the container. The PID, CPU,
// and memory limits are the runtime's. The network policy is a flag. A command
// that ignores its timeout is killed with the container rather than left behind.
//
// The container gets the project's source writable and nothing else. No Docker
// socket, no other project's volume, no host path but the one. A build has to be
// able to write its output, which is exactly why the runner is separate from the
// indexer: the indexer's mount is read-only and stays that way.
//
// Nothing here decides *what* may run. That is internal/commandpolicy, and the
// separation matters: this package would happily run any string, and the only
// reason it never does is that the string reaching it has been approved by name.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/internal/containerruntime"
)

// DefaultLimits are the shipped guardrails.
//
// Memory is capped here, unlike for the indexer. The reasoning is opposite in
// each case: killing an indexer mid-build leaves the index no better off, while
// killing a runaway test is precisely what should happen rather than letting it
// take the machine down with it.
var DefaultLimits = Limits{
	CPUs:           2,
	MemoryMB:       2048,
	PIDs:           512,
	Timeout:        10 * time.Minute,
	MaxOutputBytes: 1 << 20,
}

// Limits bound one run.
type Limits struct {
	// CPUs limits processor share. Zero means no limit.
	CPUs float64
	// MemoryMB caps memory. Zero means no cap.
	MemoryMB int
	// PIDs caps the process count, which is what stops a fork bomb from being
	// the machine's problem rather than the container's.
	PIDs int
	// Timeout bounds the whole run.
	Timeout time.Duration
	// MaxOutputBytes bounds each of stdout and stderr.
	MaxOutputBytes int
}

func (l Limits) withDefaults() Limits {
	if l.PIDs <= 0 {
		l.PIDs = DefaultLimits.PIDs
	}
	if l.Timeout <= 0 {
		l.Timeout = DefaultLimits.Timeout
	}
	if l.MaxOutputBytes <= 0 {
		l.MaxOutputBytes = DefaultLimits.MaxOutputBytes
	}
	return l
}

// Request is one command to run.
type Request struct {
	ProjectID string
	// Workspace is the host path mounted writable at /workspace.
	Workspace string
	Image     string
	// Command is a shell line. It reaches here only because a human approved it.
	Command string
	// Network is "none" or "full". Anything else is treated as none.
	Network string
	// NetworkName is the project's own runtime network, used when Network is
	// "full". A project network rather than the default bridge, so a command with
	// egress still cannot see another project's services.
	NetworkName string
	Limits      Limits
}

// Result is what happened.
type Result struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// StdoutTruncated and StderrTruncated say the stream was cut at the limit.
	StdoutTruncated bool `json:"stdout_truncated,omitempty"`
	StderrTruncated bool `json:"stderr_truncated,omitempty"`
	// TimedOut says the command was killed for exceeding its budget rather than
	// having exited on its own. A caller must be able to tell those apart: one is
	// a failing test, the other is a test that never finished.
	TimedOut bool          `json:"timed_out,omitempty"`
	Duration time.Duration `json:"-"`
	Seconds  float64       `json:"seconds"`
	// Image, Network, and Command record what actually ran, so a result can be
	// read months later without reconstructing the policy that produced it.
	Image   string `json:"image"`
	Network string `json:"network"`
	Command string `json:"command"`
}

// Errors a caller can act on.
var (
	// ErrRuntimeUnavailable means the container runtime did not answer.
	ErrRuntimeUnavailable = errors.New("the container runtime is unavailable")
	// ErrImageMissing means the approved image is not present locally.
	ErrImageMissing = errors.New("the runner image is not available")
)

// Runtime executes container-runtime commands. It is an interface so the runner can be
// exercised without a container runtime.
type Runtime interface {
	Run(ctx context.Context, stdin string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// Runner executes approved commands.
type Runner struct {
	Runtime Runtime
	Now     func() time.Time
	// Name builds the container name for a run. It is injectable so a test can
	// make names deterministic.
	Name func(projectID string, at time.Time) string
}

// New returns a runner backed by LCTK's private Podman client and connection.
func New() *Runner {
	return &Runner{Runtime: cli{}, Now: time.Now, Name: containerName}
}

// Run executes one command and returns what happened.
//
// A non-zero exit code is a result, not an error. A failing test is the ordinary
// case, and reporting it as an error would make the caller unable to tell it from
// the runtime being down.
func (r *Runner) Run(ctx context.Context, request Request) (Result, error) {
	limits := request.Limits.withDefaults()
	started := r.now()
	name := r.name(request.ProjectID, started)
	runtimeWorkspace, err := containerruntime.HostPath(request.Workspace)
	if err != nil {
		return Result{}, fmt.Errorf("prepare runner workspace: %w", err)
	}
	request.Workspace = runtimeWorkspace

	runCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()

	// Removal is deferred on its own context, because the run context is the one
	// that just expired and a cleanup that inherits a dead deadline never runs.
	defer func() {
		removeCtx, removeCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer removeCancel()
		_, _, _, _ = r.Runtime.Run(removeCtx, "", "rm", "--force", "--volumes", name)
	}()

	args := r.arguments(name, request, limits)
	stdout, stderr, exitCode, err := r.Runtime.Run(runCtx, "", args...)

	elapsed := r.now().Sub(started)
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)

	if err != nil && exitCode < 0 && !timedOut {
		return Result{}, classify(err, stderr)
	}

	stdout, stdoutCut := bound(stdout, limits.MaxOutputBytes)
	stderr, stderrCut := bound(stderr, limits.MaxOutputBytes)

	result := Result{
		ExitCode:        exitCode,
		Stdout:          stdout,
		Stderr:          stderr,
		StdoutTruncated: stdoutCut,
		StderrTruncated: stderrCut,
		TimedOut:        timedOut,
		Duration:        elapsed,
		Seconds:         elapsed.Seconds(),
		Image:           request.Image,
		Network:         networkPolicy(request.Network),
		Command:         request.Command,
	}
	if timedOut {
		// The exit code from a killed subprocess says nothing useful, and a
		// caller that reads it as the command's own would draw the wrong
		// conclusion from it.
		result.ExitCode = -1
	}
	return result, nil
}

// arguments builds the Podman invocation.
//
// Every flag here is a guardrail from docs/security.md, and the list is meant to
// be read as one: one mount and no others, a fixed working directory, an
// explicit network, and caps on processes, memory, and CPU.
func (r *Runner) arguments(name string, request Request, limits Limits) []string {
	args := []string{
		"run",
		"--name", name,
		// Not --rm: the exit code is read back from the container before it is
		// removed, and --rm would race the read.
		"--interactive=false",
		"--tty=false",
		// The one mount. A build must be able to write its output, which is why
		// this is the runner and not the indexer.
		"--volume", request.Workspace + ":/workspace",
		"--workdir", "/workspace",
		"--pids-limit", strconv.Itoa(limits.PIDs),
		"--label", "tech.lctk.project-id=" + request.ProjectID,
		"--label", "tech.lctk.role=runner",
		"--label", "tech.lctk.managed=true",
	}

	if networkPolicy(request.Network) == "full" && request.NetworkName != "" {
		// The project's own network rather than the default bridge, so a command
		// with egress still cannot reach another project's services.
		args = append(args, "--network", request.NetworkName)
	} else {
		args = append(args, "--network", "none")
	}
	if limits.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(limits.CPUs, 'f', -1, 64))
	}
	if limits.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(limits.MemoryMB)+"m")
	}

	// The command is passed to a shell because that is what it is: a line a
	// developer would type. It is safe to do so only because nothing reaches here
	// that a human has not read and approved by name.
	args = append(args, "--entrypoint", "/bin/sh", request.Image, "-c", request.Command)
	return args
}

func networkPolicy(policy string) string {
	if policy == "full" {
		return "full"
	}
	return "none"
}

// bound trims a stream to its limit, reporting whether anything was lost.
func bound(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	// The tail is kept rather than the head: a build that fails says why at the
	// end, and a caller reading a truncated log wants the failure, not the
	// banner.
	return text[len(text)-limit:], true
}

func classify(err error, stderr string) error {
	message := strings.ToLower(stderr + " " + err.Error())
	switch {
	case strings.Contains(message, "cannot connect to podman"),
		strings.Contains(message, "unable to connect to podman"),
		strings.Contains(message, "podman machine") && strings.Contains(message, "not running"),
		strings.Contains(message, "executable file not found"):
		return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, firstLine(stderr))
	case strings.Contains(message, "unable to find image"),
		strings.Contains(message, "manifest unknown"),
		strings.Contains(message, "pull access denied"):
		return fmt.Errorf("%w: %s", ErrImageMissing, firstLine(stderr))
	default:
		return fmt.Errorf("run the command: %s", firstLine(stderr+" "+err.Error()))
	}
}

func firstLine(message string) string {
	trimmed := strings.TrimSpace(message)
	if index := strings.IndexAny(trimmed, "\r\n"); index >= 0 {
		return strings.TrimSpace(trimmed[:index])
	}
	return trimmed
}

func (r *Runner) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Runner) name(projectID string, at time.Time) string {
	if r.Name == nil {
		return containerName(projectID, at)
	}
	return r.Name(projectID, at)
}

// containerName is unique per run so two commands in one project cannot collide.
func containerName(projectID string, at time.Time) string {
	return "lctk-" + projectID + "-run-" + strconv.FormatInt(at.UnixNano(), 36)
}

// cli runs the installation-owned Podman executable against the explicit
// LCTK connection; no ambient PATH or default connection is trusted.
type cli struct{}

func (cli) Run(ctx context.Context, stdin string, args ...string) (string, string, int, error) {
	command, err := containerruntime.Command(ctx, args...)
	if err != nil {
		return "", "", -1, err
	}
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err = command.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
			err = nil
		} else {
			exitCode = -1
		}
	}
	return stdout.String(), stderr.String(), exitCode, err
}
