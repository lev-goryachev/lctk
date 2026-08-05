package projectstack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/lev-goryachev/lctk/internal/hostsettings"
	"github.com/lev-goryachev/lctk/internal/inference"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

// State is the observable lifecycle state of a project stack.
//
// The vocabulary comes from docs/project-lifecycle.md. It is deliberately coarse:
// an agent consuming this needs to know whether to retry, wait, or give up, not
// the internal details of the container runtime.
type State string

const (
	// StateStopped means the project is registered and nothing is running.
	StateStopped State = "stopped"
	// StateStarting means containers exist but health has not been confirmed.
	StateStarting State = "starting"
	// StateRunning means the service is up and reporting healthy.
	StateRunning State = "running"
	// StateError means containers exist in a failed or unexpected condition.
	StateError State = "error"
	// StateUnknown means the runtime could not be queried at all.
	StateUnknown State = "unknown"
)

// Retryable reports whether waiting and asking again is worthwhile. It exists so
// a typed error can carry the same advice to a calling agent.
func (s State) Retryable() bool { return s == StateStarting }

// Errors a caller is expected to distinguish.
var (
	// ErrRuntimeUnavailable reports that the container runtime cannot be reached.
	// It is separate from a project failure so a caller does not blame the
	// project for Docker Desktop being closed.
	ErrRuntimeUnavailable = errors.New("container runtime is unavailable")
	// ErrLinuxContainersRequired reports a reachable runtime that cannot run
	// Linux containers, which Docker Desktop on Windows does when switched to
	// Windows container mode.
	//
	// LCTK project stacks are Linux containers by design: ADR-0011 requires a
	// Linux boundary for the search backend. Without this check the failure
	// surfaces much later as an opaque image-manifest error.
	ErrLinuxContainersRequired = errors.New("container runtime must be able to run Linux containers")
	// ErrImageMissing reports that the reusable image has not been built.
	ErrImageMissing = errors.New("reusable code-intel image is not available")
)

// serviceHost is where a published project port is reachable. It is loopback
// only: a project service must not be exposed to the network.
const serviceHost = "127.0.0.1"

// servicePortKey is how the runtime names the container port in its port map.
var servicePortKey = strconv.Itoa(ServicePort) + "/tcp"

// Status is the runtime view of one project stack.
type Status struct {
	ProjectID string `json:"project_id"`
	State     State  `json:"state"`
	Health    string `json:"health,omitempty"`
	Container string `json:"container,omitempty"`
	Image     string `json:"image"`
	Network   string `json:"network"`
	Volume    string `json:"volume"`
	// ServiceAddress is the loopback host address the project's code-intel
	// service is published on, empty when the project is not running. The port is
	// assigned by the runtime rather than chosen by LCTK, so it changes across a
	// restart and must be read rather than remembered.
	ServiceAddress string `json:"service_address,omitempty"`
	// Detail explains an error or transitional state in one line.
	Detail string `json:"detail,omitempty"`
}

// parseInspect splits the delimited inspect output, tolerating the older
// whitespace-separated form so a container started by a previous build is still
// readable rather than reported as unparseable.
func parseInspect(stdout string) []string {
	trimmed := strings.TrimSpace(stdout)
	if strings.Contains(trimmed, "|") {
		return strings.Split(trimmed, "|")
	}
	return strings.Fields(trimmed)
}

// Runner executes container-runtime commands. It is an interface so the lifecycle
// logic can be tested without Docker, and so the transport can change without
// touching callers.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout string, stderr string, err error)
}

// dockerRunner shells out to the Docker CLI.
//
// Compose orchestration goes through the CLI rather than the Moby API because the
// Compose specification is implemented there, and ADR-0003 commits LCTK to
// Compose projects. Inspection also goes through the CLI so that a single
// mechanism explains any failure.
type dockerRunner struct{}

func (dockerRunner) Run(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

type inferenceLifecycle interface {
	Ensure(context.Context, time.Duration) (inference.Status, error)
}

// Manager drives project stacks.
type Manager struct {
	runner Runner
	// inference is shared stateless compute and is ensured before any project
	// receives its endpoint. It is nil only in narrow lifecycle unit tests that
	// intentionally exercise Compose behavior in isolation.
	sharedInference inferenceLifecycle
	inferenceErr    error
	// settings resolves the machine's background-load policy. It is a function so
	// a change takes effect on the next start without restarting the daemon, and
	// so a test can drive a policy without writing a file.
	settings func() (hostsettings.Settings, error)
	// version selects the code-intel tag. Production defaults to the host build;
	// update sets a candidate explicitly before its health gate.
	version string
}

// NewManager returns a manager backed by the local Docker CLI.
func NewManager() *Manager {
	runner := dockerRunner{}
	shared, err := inference.NewManager(runner)
	return &Manager{runner: runner, sharedInference: shared, inferenceErr: err, settings: hostsettings.Load, version: buildinfo.Version}
}

// NewManagerWithRunner returns a manager backed by a supplied runner, for tests.
func NewManagerWithRunner(runner Runner) *Manager {
	return &Manager{runner: runner, settings: hostsettings.Load, version: buildinfo.Version}
}

// WithVersion selects the candidate code-intel tag for transactional update.
func (m *Manager) WithVersion(version string) *Manager {
	m.version = version
	return m
}

func (m *Manager) names(projectID string) (Names, error) {
	return DeriveNamesForVersion(projectID, m.version)
}

// WithInference injects the shared lifecycle for tests that verify ordering.
// Production uses NewManager, which always configures the pinned implementation.
func (m *Manager) WithInference(shared inferenceLifecycle) *Manager {
	m.sharedInference = shared
	m.inferenceErr = nil
	return m
}

// WithSettings replaces the policy source, for tests and for callers that have
// already resolved it.
func (m *Manager) WithSettings(settings func() (hostsettings.Settings, error)) *Manager {
	m.settings = settings
	return m
}

// Budget resolves what this project is allowed to cost: the machine policy, with
// the project's own mode layered on top when it has one.
func (m *Manager) Budget(project projectregistry.Project) hostsettings.Budget {
	load := hostsettings.Defaults
	if m.settings != nil {
		if resolved, err := m.settings(); err == nil {
			load = resolved
		}
	}
	return load.Resources.WithProjectMode(hostsettings.Mode(project.ResourceMode)).Budget()
}

// RuntimeAvailable reports whether the container runtime answers and can run the
// Linux containers LCTK needs.
//
// Reachability alone is not enough. A Windows host can have a running daemon in
// Windows container mode, which answers every query and then rejects a Linux
// image with an opaque manifest error. Reporting that here turns a confusing
// late failure into an actionable one, and lets callers skip rather than fail
// where no suitable runtime exists.
func (m *Manager) RuntimeAvailable(ctx context.Context) error {
	stdout, stderr, err := m.runner.Run(ctx, "version", "--format", "{{.Server.Version}} {{.Server.Os}}")
	if err != nil {
		return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, firstLine(stderr, err))
	}

	fields := strings.Fields(strings.TrimSpace(stdout))
	if len(fields) < 2 {
		// An older or unusual runtime that does not report its OS is accepted:
		// refusing on a parse failure would be worse than letting the image pull
		// report the real problem.
		return nil
	}
	if serverOS := strings.ToLower(fields[1]); serverOS != "linux" {
		return fmt.Errorf("%w: the runtime reports %q; switch Docker Desktop to Linux containers",
			ErrLinuxContainersRequired, serverOS)
	}
	return nil
}

// ImageAvailable reports whether the reusable image exists locally.
func (m *Manager) ImageAvailable(ctx context.Context, image string) error {
	_, found, err := m.inspectImageID(ctx, image)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrImageMissing, image)
	}
	return nil
}

// ImageMatches proves that the Compose version tag resolves to the exact
// signed digest selected by a release manifest. Existence of the tag alone is
// insufficient because local tags are mutable Docker state.
func (m *Manager) ImageMatches(ctx context.Context, tag, immutableReference string) (bool, error) {
	tagID, tagFound, err := m.inspectImageID(ctx, tag)
	if err != nil || !tagFound {
		return false, err
	}
	referenceID, referenceFound, err := m.inspectImageID(ctx, immutableReference)
	if err != nil || !referenceFound {
		return false, err
	}
	return tagID == referenceID, nil
}

func (m *Manager) inspectImageID(ctx context.Context, image string) (string, bool, error) {
	stdout, stderr, err := m.runner.Run(ctx, "image", "inspect", image, "--format", "{{.Id}}")
	if err != nil {
		if isNoSuchImage(stdout, stderr) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect image %s: %s", image, firstLine(stderr, err))
	}
	id := strings.TrimSpace(stdout)
	if id == "" {
		return "", false, fmt.Errorf("inspect image %s returned no identity", image)
	}
	return id, true, nil
}

// InstallImage pulls an immutable OCI reference, then assigns the local unified
// version tag consumed by Compose. The tag is derived state; the signed digest
// remains the authority checked by the pull.
func (m *Manager) InstallImage(ctx context.Context, immutableReference, version string) error {
	if immutableReference == "" || version == "" {
		return errors.New("code-intel image reference or version is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, stderr, err := m.runner.Run(ctx, "pull", immutableReference); err != nil {
		return fmt.Errorf("pull code-intel image: %s", firstLine(stderr, err))
	}
	tag := ImageRepository + ":" + version
	if _, stderr, err := m.runner.Run(ctx, "tag", immutableReference, tag); err != nil {
		return fmt.Errorf("tag code-intel image %s: %s", tag, firstLine(stderr, err))
	}
	return nil
}

// RestoreSchemaRollback swaps the fixed semantic database rollback bundle back
// into place inside one project volume. It is invoked only after the candidate
// container has been stopped; source remains unmounted and the helper has no
// network access.
func (m *Manager) RestoreSchemaRollback(ctx context.Context, project projectregistry.Project, version string) error {
	names, err := DeriveNamesForVersion(project.ID, version)
	if err != nil {
		return err
	}
	script := `set -eu
db=/state/semantic/semantic.db
rollback=${db}.rollback-v1
failed=${db}.failed-update
test -f "$rollback" || exit 0
# A hardlink preserves the displaced candidate without copying it. The final
# rename replaces semantic.db atomically, so a crash sees either the complete
# current database or the complete rollback database and never a missing name.
ln -f "$db" "$failed"
mv -f "$rollback" "$db"`
	args := []string{
		"run", "--rm", "--network", "none", "--entrypoint", "/bin/sh",
		"--mount", "type=volume,source=" + names.Volume + ",target=/state",
		names.Image, "-c", script,
	}
	if _, stderr, err := m.runner.Run(ctx, args...); err != nil {
		return fmt.Errorf("restore schema rollback for %s: %s", project.ID, firstLine(stderr, err))
	}
	return nil
}

// BuildImage builds the reusable image from a build context directory.
//
// The image is shared by every project, so it is built once per product version
// rather than per project.
func (m *Manager) BuildImage(ctx context.Context, image, contextDir string) error {
	_, stderr, err := m.runner.Run(ctx, "build", "--tag", image, contextDir)
	if err != nil {
		return fmt.Errorf("build %s: %s", image, firstLine(stderr, err))
	}
	return nil
}

// Start writes the Compose file and brings the stack up, then waits for health.
//
// The Compose file is rewritten on every start so that a change in the registry
// or in the product version takes effect, and so a hand-edited file cannot
// persist.
func (m *Manager) Start(ctx context.Context, project projectregistry.Project, wait time.Duration) (Status, error) {
	names, err := m.names(project.ID)
	if err != nil {
		return Status{}, err
	}
	if err := m.RuntimeAvailable(ctx); err != nil {
		return Status{}, err
	}
	if err := m.ImageAvailable(ctx, names.Image); err != nil {
		return Status{}, err
	}
	if m.inferenceErr != nil {
		return Status{}, m.inferenceErr
	}
	if m.sharedInference != nil {
		if _, err := m.sharedInference.Ensure(ctx, wait); err != nil {
			return Status{}, err
		}
	}

	composePath, err := WriteForVersion(project, m.Budget(project), m.version)
	if err != nil {
		return Status{}, err
	}

	if _, stderr, err := m.runner.Run(ctx, m.composeArgs(composePath, names, "up", "--detach")...); err != nil {
		return Status{}, fmt.Errorf("start %s: %s", project.ID, firstLine(stderr, err))
	}

	return m.waitForHealth(ctx, names, wait)
}

// Stop takes the stack down while preserving the project volume.
//
// This is the stop of docs/project-lifecycle.md, not purge: containers and the
// network go away, persistent state does not.
func (m *Manager) Stop(ctx context.Context, project projectregistry.Project) (Status, error) {
	names, err := m.names(project.ID)
	if err != nil {
		return Status{}, err
	}
	if err := m.RuntimeAvailable(ctx); err != nil {
		return Status{}, err
	}

	composePath, err := ComposeFilePath(project.ID)
	if err != nil {
		return Status{}, err
	}

	// "down" without --volumes deliberately keeps the named volume, so indexes
	// and memory survive. Removing them is a separate, explicit purge.
	if _, stderr, err := m.runner.Run(ctx, m.composeArgs(composePath, names, "down", "--remove-orphans")...); err != nil {
		return Status{}, fmt.Errorf("stop %s: %s", project.ID, firstLine(stderr, err))
	}

	return Status{
		ProjectID: project.ID,
		State:     StateStopped,
		Image:     names.Image,
		Network:   names.Network,
		Volume:    names.Volume,
	}, nil
}

// Restart stops and starts the stack, preserving the project volume.
func (m *Manager) Restart(ctx context.Context, project projectregistry.Project, wait time.Duration) (Status, error) {
	if _, err := m.Stop(ctx, project); err != nil {
		return Status{}, err
	}
	return m.Start(ctx, project, wait)
}

// Status reports the current runtime state without changing it.
func (m *Manager) Status(ctx context.Context, project projectregistry.Project) (Status, error) {
	names, err := m.names(project.ID)
	if err != nil {
		return Status{}, err
	}

	status := Status{
		ProjectID: project.ID,
		State:     StateUnknown,
		Image:     names.Image,
		Network:   names.Network,
		Volume:    names.Volume,
	}

	if err := m.RuntimeAvailable(ctx); err != nil {
		status.Detail = err.Error()
		return status, err
	}
	return m.inspect(ctx, names)
}

// inspect derives the lifecycle state from the container's own reported state and
// health. A container that does not exist means stopped, which is the normal
// resting state of a registered project.
func (m *Manager) inspect(ctx context.Context, names Names) (Status, error) {
	status := Status{
		ProjectID: names.ProjectID,
		Image:     names.Image,
		Network:   names.Network,
		Volume:    names.Volume,
		Container: names.ContainerName,
	}

	// One inspect answers three questions, so a search does not cost an extra
	// runtime call to find the service. Fields are separated explicitly rather
	// than by whitespace because health and the published port are both allowed
	// to be empty, and positional parsing would silently shift.
	format := "{{.State.Status}}|" +
		"{{if .State.Health}}{{.State.Health.Status}}{{end}}|" +
		`{{with index .NetworkSettings.Ports "` + servicePortKey + `"}}{{(index . 0).HostPort}}{{end}}`
	stdout, stderr, err := m.runner.Run(ctx, "inspect", names.ContainerName, "--format", format)
	if err != nil {
		if isNoSuchContainer(stdout, stderr) {
			status.State = StateStopped
			status.Container = ""
			return status, nil
		}
		status.State = StateUnknown
		status.Detail = firstLine(stderr, err)
		return status, fmt.Errorf("inspect %s: %s", names.ContainerName, status.Detail)
	}

	fields := parseInspect(stdout)
	containerState := ""
	if len(fields) > 0 {
		containerState = fields[0]
	}
	if len(fields) > 1 {
		status.Health = fields[1]
	}
	if len(fields) > 2 && fields[2] != "" {
		status.ServiceAddress = net.JoinHostPort(serviceHost, fields[2])
	}

	switch containerState {
	case "running":
		switch status.Health {
		case "healthy":
			status.State = StateRunning
		case "starting", "":
			status.State = StateStarting
			status.Detail = "container is up, health not yet confirmed"
		case "unhealthy":
			status.State = StateError
			status.Detail = "container reports unhealthy"
		default:
			status.State = StateStarting
			status.Detail = "unrecognized health status " + status.Health
		}
	case "created", "restarting":
		status.State = StateStarting
		status.Detail = "container is " + containerState
	case "exited", "dead", "removing", "paused":
		status.State = StateStopped
		status.Detail = "container is " + containerState
	case "":
		status.State = StateUnknown
		status.Detail = "container state could not be parsed"
	default:
		status.State = StateUnknown
		status.Detail = "unrecognized container state " + containerState
	}
	return status, nil
}

// waitForHealth polls until the stack is running, giving up after the budget.
//
// A timeout returns the last observed status together with a typed error, so a
// caller learns whether it was still starting, which is retryable, or failed.
func (m *Manager) waitForHealth(ctx context.Context, names Names, budget time.Duration) (Status, error) {
	if budget <= 0 {
		return m.inspect(ctx, names)
	}
	deadline := time.Now().Add(budget)

	var status Status
	for {
		var err error
		status, err = m.inspect(ctx, names)
		if err != nil {
			return status, err
		}
		switch status.State {
		case StateRunning:
			return status, nil
		case StateError:
			return status, fmt.Errorf("project %s failed to start: %s", names.ProjectID, status.Detail)
		}
		if !time.Now().Before(deadline) {
			return status, fmt.Errorf("project %s did not become healthy within %s (state %s)",
				names.ProjectID, budget, status.State)
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// composeArgs builds a docker compose invocation pinned to the generated file and
// the project's Compose name, so it can never act on a different stack.
func (m *Manager) composeArgs(composePath string, names Names, action ...string) []string {
	args := []string{"compose", "--file", composePath, "--project-name", names.ComposeName}
	return append(args, action...)
}

func isNoSuchContainer(stdout, stderr string) bool {
	combined := strings.ToLower(stdout + " " + stderr)
	return strings.Contains(combined, "no such object") ||
		strings.Contains(combined, "no such container")
}

func isNoSuchImage(stdout, stderr string) bool {
	combined := strings.ToLower(stdout + " " + stderr)
	return strings.Contains(combined, "no such image") ||
		strings.Contains(combined, "no such object")
}

func firstLine(stderr string, err error) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		if err != nil {
			return err.Error()
		}
		return "no output"
	}
	if index := strings.IndexAny(trimmed, "\r\n"); index > 0 {
		return trimmed[:index]
	}
	return trimmed
}
