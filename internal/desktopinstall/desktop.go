// Package desktopinstall installs the stable launcher and delegates Windows
// sign-in and Start-menu registration to the platform implementation.
package desktopinstall

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/lev-goryachev/lctk/internal/releasebundle"
	"github.com/lev-goryachev/lctk/internal/verifieddownload"
)

// Manager owns the user-visible desktop payload under one installation home.
type Manager struct {
	Home     string
	Client   *http.Client
	Register func(launcher, uninstaller, version string) error
}

// Plan is the read-only stable-launcher installation decision.
type Plan struct {
	LauncherInstalled bool  `json:"launcher_installed"`
	SetupInstalled    bool  `json:"setup_installed"`
	DownloadBytes     int64 `json:"download_bytes"`
}

// NewManager returns a production desktop installer.
func NewManager(home string) *Manager {
	return &Manager{Home: home, Client: http.DefaultClient, Register: registerDesktop}
}

// Inspect determines whether the exact signed launcher is already active.
func (m *Manager) Inspect(manifest releasebundle.Manifest) (Plan, error) {
	launcherArtifact, err := manifest.ArtifactFor("host-launcher", "windows", "amd64")
	if err != nil {
		return Plan{}, err
	}
	setupArtifact, err := manifest.ArtifactFor("installer", "windows", "amd64")
	if err != nil {
		return Plan{}, err
	}
	launcher := filepath.Join(m.Home, "bin", "lctk.exe")
	setup := filepath.Join(m.Home, "bin", "lctk-setup.exe")
	launcherInstalled := verifieddownload.Verify(launcher, launcherArtifact.Bytes, launcherArtifact.SHA256) == nil
	setupInstalled := verifieddownload.Verify(setup, setupArtifact.Bytes, setupArtifact.SHA256) == nil
	var download int64
	if !launcherInstalled {
		download += launcherArtifact.Bytes
	}
	if !setupInstalled {
		download += setupArtifact.Bytes
	}
	return Plan{LauncherInstalled: launcherInstalled, SetupInstalled: setupInstalled, DownloadBytes: download}, nil
}

// Install downloads the signed stable launcher, verifies it, and registers the
// sign-in daemon and Start-menu entry only after the file is active.
func (m *Manager) Install(ctx context.Context, manifest releasebundle.Manifest) (string, error) {
	if m.Home == "" || m.Client == nil || m.Register == nil {
		return "", errors.New("desktop installer is incomplete")
	}
	launcherArtifact, err := manifest.ArtifactFor("host-launcher", "windows", "amd64")
	if err != nil {
		return "", err
	}
	setupArtifact, err := manifest.ArtifactFor("installer", "windows", "amd64")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(m.Home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return "", fmt.Errorf("create desktop program directory: %w", err)
	}
	launcher := filepath.Join(bin, "lctk.exe")
	if verifieddownload.Verify(launcher, launcherArtifact.Bytes, launcherArtifact.SHA256) != nil {
		if err := verifieddownload.Download(ctx, m.Client, launcherArtifact, launcher); err != nil {
			return "", err
		}
	}
	setup := filepath.Join(bin, "lctk-setup.exe")
	if verifieddownload.Verify(setup, setupArtifact.Bytes, setupArtifact.SHA256) != nil {
		if err := verifieddownload.Download(ctx, m.Client, setupArtifact, setup); err != nil {
			return "", err
		}
	}
	if err := m.Register(launcher, setup, manifest.Version); err != nil {
		return "", err
	}
	return launcher, nil
}

// Unregister removes user-facing launch points without deleting project data.
func Unregister() error { return unregisterDesktop() }
