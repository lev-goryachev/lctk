// Package runtimeapi exposes the read-only health identity of LCTK's managed
// container runtime without binding callers to Podman's command-line schema.
package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lev-goryachev/lctk/internal/containerruntime"
)

// Status is the stable operator-facing runtime identity.
type Status struct {
	Available bool   `json:"available"`
	Provider  string `json:"provider,omitempty"`
	Version   string `json:"version,omitempty"`
	OSType    string `json:"os_type,omitempty"`
}

// Runner is the read-only Podman command boundary used by ProbeWithRunner.
type Runner interface {
	Run(context.Context, ...string) (string, string, error)
}

// Probe verifies the installation-owned client, explicit connection, Linux
// machine identity, and server version in one non-mutating call.
func Probe(ctx context.Context) (Status, error) {
	return ProbeWithRunner(ctx, containerruntime.Runner{})
}

// ProbeWithRunner performs the probe through an injectable command boundary.
func ProbeWithRunner(ctx context.Context, runner Runner) (Status, error) {
	stdout, stderr, err := runner.Run(ctx, "info", "--format", "json")
	if err != nil {
		return Status{}, fmt.Errorf("managed Podman runtime is unavailable: %s: %w", firstLine(stderr), err)
	}
	var info struct {
		Host struct {
			OS string `json:"os"`
		} `json:"host"`
		Version struct {
			Version string `json:"Version"`
		} `json:"version"`
	}
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		return Status{}, fmt.Errorf("decode managed Podman identity: %w", err)
	}
	if !strings.EqualFold(info.Host.OS, "linux") {
		return Status{}, fmt.Errorf("managed Podman runtime reported unsupported OS %q", info.Host.OS)
	}
	version := strings.TrimSpace(info.Version.Version)
	if version == "" {
		version = containerruntime.Version
	}
	return Status{Available: true, Provider: containerruntime.Provider, Version: version, OSType: "linux"}, nil
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexAny(value, "\r\n"); index >= 0 {
		return strings.TrimSpace(value[:index])
	}
	return value
}
