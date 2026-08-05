// Package runtimeinstall owns the verified on-disk installation and machine
// lifecycle of the private Podman runtime selected by ADR-0023.
package runtimeinstall

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lev-goryachev/lctk/internal/containerruntime"
	"github.com/lev-goryachev/lctk/internal/diskspace"
	"github.com/lev-goryachev/lctk/internal/releasebundle"
	"github.com/lev-goryachev/lctk/internal/verifieddownload"
)

const diskMargin = 512 << 20

// Plan is the complete read-only runtime installation decision shown before
// setup mutates files or creates the managed WSL machine.
type Plan struct {
	Version        string      `json:"version"`
	Components     []Component `json:"components"`
	DownloadBytes  int64       `json:"download_bytes"`
	RequiredBytes  int64       `json:"required_bytes"`
	AvailableBytes uint64      `json:"available_bytes"`
	Ready          bool        `json:"ready"`
	Writes         bool        `json:"writes"`
}

// Component records one exact release artifact and whether its verified target
// already exists.
type Component struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Installed bool   `json:"installed"`
}

// MachineRunner executes local Podman machine commands. The abstraction keeps
// archive and transaction tests independent from WSL.
type MachineRunner interface {
	Run(context.Context, ...string) (string, string, error)
}

// Manager installs only beneath Home and never trusts ambient Podman state.
type Manager struct {
	Home       string
	Client     *http.Client
	Available  func(string) (uint64, error)
	Machine    MachineRunner
	TargetOS   string
	TargetArch string
}

// NewManager returns the production runtime installer.
func NewManager(home string) *Manager {
	return &Manager{Home: home, Client: http.DefaultClient, Available: diskspace.Available, Machine: machineCLI{}, TargetOS: runtime.GOOS, TargetArch: runtime.GOARCH}
}

// Inspect verifies the manifest selection, existing identities, and disk budget
// without creating a directory or invoking Podman.
func (m *Manager) Inspect(manifest releasebundle.Manifest) (Plan, error) {
	if m.TargetOS != "windows" || m.TargetArch != "amd64" {
		return Plan{}, fmt.Errorf("managed runtime setup supports windows/amd64, not %s/%s", m.TargetOS, m.TargetArch)
	}
	if m.Home == "" || m.Client == nil || m.Available == nil || m.Machine == nil {
		return Plan{}, errors.New("runtime installer is incomplete")
	}
	artifacts, err := runtimeArtifacts(manifest)
	if err != nil {
		return Plan{}, err
	}
	available, err := m.Available(m.Home)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Version: containerruntime.Version, AvailableBytes: available}
	for _, artifact := range artifacts {
		target := m.artifactTarget(artifact.Kind)
		installed := verifieddownload.Verify(target, artifact.Bytes, artifact.SHA256) == nil
		plan.Components = append(plan.Components, Component{Kind: artifact.Kind, Name: artifact.Name, Bytes: artifact.Bytes, SHA256: artifact.SHA256, Installed: installed})
		if !installed {
			plan.DownloadBytes += artifact.Bytes
		}
	}
	plan.RequiredBytes = plan.DownloadBytes*2 + diskMargin
	plan.Ready = available >= uint64(plan.RequiredBytes)
	return plan, nil
}

// Install downloads verified runtime bytes, activates the portable client, and
// creates or starts exactly the installation-owned Podman machine.
func (m *Manager) Install(ctx context.Context, manifest releasebundle.Manifest) error {
	plan, err := m.Inspect(manifest)
	if err != nil {
		return err
	}
	if !plan.Ready {
		return fmt.Errorf("runtime installation requires %d bytes; only %d are available", plan.RequiredBytes, plan.AvailableBytes)
	}
	artifacts, err := runtimeArtifacts(manifest)
	if err != nil {
		return err
	}
	clientArchive := m.artifactTarget("podman-client")
	machineImage := m.artifactTarget("podman-machine")
	for _, artifact := range artifacts {
		target := m.artifactTarget(artifact.Kind)
		if verifieddownload.Verify(target, artifact.Bytes, artifact.SHA256) == nil {
			continue
		}
		if err := verifieddownload.Download(ctx, m.Client, artifact, target); err != nil {
			return err
		}
	}
	if err := m.installClient(clientArchive); err != nil {
		return err
	}
	if err := m.ensureMachine(ctx, machineImage); err != nil {
		return err
	}
	return nil
}

func runtimeArtifacts(manifest releasebundle.Manifest) ([]releasebundle.Artifact, error) {
	client, err := manifest.ArtifactFor("podman-client", "windows", "amd64")
	if err != nil {
		return nil, err
	}
	machine, err := manifest.ArtifactFor("podman-machine", "linux", "amd64")
	if err != nil {
		return nil, err
	}
	return []releasebundle.Artifact{client, machine}, nil
}

func (m *Manager) artifactTarget(kind string) string {
	base := filepath.Join(m.Home, "runtime", containerruntime.Provider, containerruntime.Version)
	if kind == "podman-client" {
		return filepath.Join(base, "downloads", "podman-windows-amd64.zip")
	}
	return filepath.Join(base, "machine", "podman-machine.x86_64.wsl.tar.zst")
}

func (m *Manager) installClient(archivePath string) error {
	targetDir := filepath.Join(m.Home, "runtime", containerruntime.Provider, containerruntime.Version, "bin")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return fmt.Errorf("create Podman client directory: %w", err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open Podman client archive: %w", err)
	}
	defer archive.Close()
	wanted := map[string]bool{"podman.exe": false, "gvproxy.exe": false, "win-sshproxy.exe": false}
	for _, entry := range archive.File {
		// ZIP paths use forward slashes. Reject every non-canonical entry before
		// inspecting or extracting it so traversal syntax and Windows separators
		// cannot cross the installation-owned runtime directory.
		archiveName := strings.TrimSuffix(entry.Name, "/")
		if archiveName == "" || strings.Contains(entry.Name, `\`) || !fs.ValidPath(archiveName) {
			return fmt.Errorf("Podman client archive has an unsafe path %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			continue
		}

		// Map the untrusted archive basename to one of three constant filenames.
		// The archive path itself is never joined to a host filesystem path.
		executable := ""
		switch path.Base(archiveName) {
		case "podman.exe":
			executable = "podman.exe"
		case "gvproxy.exe":
			executable = "gvproxy.exe"
		case "win-sshproxy.exe":
			executable = "win-sshproxy.exe"
		default:
			continue
		}
		if !strings.HasSuffix(archiveName, "/usr/bin/"+executable) {
			continue
		}
		if wanted[executable] || entry.UncompressedSize64 > 256<<20 {
			return fmt.Errorf("Podman client archive has an invalid %s entry", executable)
		}
		if err := extractEntry(entry, filepath.Join(targetDir, executable)); err != nil {
			return err
		}
		wanted[executable] = true
	}
	for name, found := range wanted {
		if !found {
			return fmt.Errorf("Podman client archive omits %s", name)
		}
	}
	return nil
}

func extractEntry(entry *zip.File, target string) error {
	source, err := entry.Open()
	if err != nil {
		return fmt.Errorf("open %s from Podman archive: %w", entry.Name, err)
	}
	defer source.Close()
	temporary := target + ".new"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("create temporary Podman executable: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, int64(entry.UncompressedSize64)+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != int64(entry.UncompressedSize64) {
		_ = os.Remove(temporary)
		return fmt.Errorf("extract Podman executable %s", entry.Name)
	}
	if err := verifieddownload.Activate(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("activate Podman executable %s: %w", target, err)
	}
	return nil
}

func (m *Manager) ensureMachine(ctx context.Context, image string) error {
	_, stderr, err := m.Machine.Run(ctx, "inspect", containerruntime.MachineName)
	if err != nil {
		message := strings.ToLower(stderr + " " + err.Error())
		if !strings.Contains(message, "does not exist") && !strings.Contains(message, "no such") {
			return fmt.Errorf("inspect managed Podman machine: %w", err)
		}
		args := []string{"init", "--image", image, "--cpus", "4", "--memory", "6144", "--disk-size", "60", "--rootful", containerruntime.MachineName}
		if _, stderr, err := m.Machine.Run(ctx, args...); err != nil {
			return fmt.Errorf("initialize managed Podman machine: %s: %w", firstLine(stderr), err)
		}
	}
	if _, stderr, err := m.Machine.Run(ctx, "start", containerruntime.MachineName); err != nil && !strings.Contains(strings.ToLower(stderr), "already running") {
		return fmt.Errorf("start managed Podman machine: %s: %w", firstLine(stderr), err)
	}
	return nil
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexAny(value, "\r\n"); index >= 0 {
		return strings.TrimSpace(value[:index])
	}
	return value
}
