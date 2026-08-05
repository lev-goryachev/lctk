// Package containerruntime owns the private Podman transport used by LCTK.
//
// The executable and connection are selected by absolute, installation-owned
// identity. LCTK never consults PATH or Podman's default connection because
// either would let unrelated user configuration change the runtime boundary
// accepted in ADR-0023.
package containerruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lev-goryachev/lctk/internal/lctkhome"
)

const (
	// Provider is the bounded OCI implementation selected by ADR-0023.
	Provider = "podman"
	// Version is the portable client and machine-OS compatibility line shipped
	// by one LCTK release. Podman does not support a host/machine version skew.
	Version = "5.8.2"
	// MachineName is the only WSL-backed Podman machine LCTK owns.
	MachineName = "lctk-runtime"
	// ConnectionName selects the root connection created for MachineName. The
	// explicit connection prevents a user's Podman default from affecting LCTK.
	ConnectionName = MachineName + "-root"
	// ExecutableOverride relocates the private client for isolated tests and
	// source-development proofs. Production setup never writes this variable.
	ExecutableOverride = "LCTK_PODMAN_PATH"
)

// ErrClientMissing distinguishes an incomplete installation from a stopped or
// unhealthy managed machine.
var ErrClientMissing = errors.New("the managed Podman client is not installed")

// ExecutablePath resolves the private client without creating any directory.
func ExecutablePath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(ExecutableOverride)); override != "" {
		absolute, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", ExecutableOverride, err)
		}
		return absolute, nil
	}
	home, err := lctkhome.Dir()
	if err != nil {
		return "", err
	}
	name := "podman"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(home, "runtime", Provider, Version, "bin", name), nil
}

// Command builds one OCI command against LCTK's explicit machine connection.
func Command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	path, err := ExecutablePath()
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(path); statErr != nil || info.IsDir() {
		if statErr == nil {
			statErr = errors.New("path is a directory")
		}
		return nil, fmt.Errorf("%w at %q: %v", ErrClientMissing, path, statErr)
	}
	selected := []string{"--connection", ConnectionName}
	selected = append(selected, args...)
	return exec.CommandContext(ctx, path, selected...), nil
}

// MachineCommand builds one local machine-lifecycle command. Machine commands
// cannot use --connection because they create and start that connection.
func MachineCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	path, err := ExecutablePath()
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(path); statErr != nil || info.IsDir() {
		if statErr == nil {
			statErr = errors.New("path is a directory")
		}
		return nil, fmt.Errorf("%w at %q: %v", ErrClientMissing, path, statErr)
	}
	selected := []string{"machine"}
	selected = append(selected, args...)
	return exec.CommandContext(ctx, path, selected...), nil
}

// Runner executes OCI commands through the private client.
type Runner struct{}

// Run captures one command without allowing inherited stdin or an interactive
// prompt. Installation and authentication failures must fail fast instead.
func (Runner) Run(ctx context.Context, args ...string) (string, string, error) {
	command, err := Command(ctx, args...)
	if err != nil {
		return "", "", err
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	return stdout.String(), stderr.String(), err
}
