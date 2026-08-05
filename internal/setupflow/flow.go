// Package setupflow coordinates the signed host, private runtime, desktop, and
// bootstrap transactions behind the one-click Windows setup UI.
package setupflow

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/lev-goryachev/lctk/internal/desktopinstall"
	"github.com/lev-goryachev/lctk/internal/installation"
	"github.com/lev-goryachev/lctk/internal/releasebundle"
	"github.com/lev-goryachev/lctk/internal/runtimeinstall"
	"github.com/lev-goryachev/lctk/internal/windowssetup"
)

// ErrRebootRequired is a successful prerequisite mutation that cannot continue
// until Windows restarts.
var ErrRebootRequired = errors.New("Windows must restart before LCTK setup can continue")

// Plan is the complete read-only setup decision shown by setup.exe.
type Plan struct {
	Version                   string              `json:"version"`
	Host                      windowssetup.Status `json:"host"`
	Runtime                   runtimeinstall.Plan `json:"runtime"`
	Core                      installation.Plan   `json:"core"`
	Desktop                   desktopinstall.Plan `json:"desktop"`
	DownloadBytes             int64               `json:"download_bytes"`
	RuntimeDataRequiredBytes  int64               `json:"runtime_data_required_bytes"`
	RuntimeDataAvailableBytes uint64              `json:"runtime_data_available_bytes"`
	Ready                     bool                `json:"ready"`
	Writes                    bool                `json:"writes"`
}

// Progress is one durable user-facing setup phase.
type Progress func(phase, detail string)

type runtimeInstaller interface {
	Inspect(releasebundle.Manifest) (runtimeinstall.Plan, error)
	Install(context.Context, releasebundle.Manifest) error
}

type coreInstaller interface {
	Inspect(releasebundle.Manifest) (installation.Plan, releasebundle.Artifact, error)
	Install(context.Context, releasebundle.Manifest) (installation.Activation, error)
}

type desktopInstaller interface {
	Inspect(releasebundle.Manifest) (desktopinstall.Plan, error)
	Install(context.Context, releasebundle.Manifest) (string, error)
}

// Manager wires the production installers while keeping the coordinator
// independently testable.
type Manager struct {
	Home           string
	ManifestSource string
	Runtime        runtimeInstaller
	Core           coreInstaller
	Desktop        desktopInstaller
	ProbeHost      func(context.Context) (windowssetup.Status, error)
	EnableWSL      func(context.Context) (bool, error)
	RegisterResume func() error
	Run            func(context.Context, string, ...string) ([]byte, error)
	Progress       Progress
}

// NewManager returns the complete production setup transaction.
func NewManager(home, manifestSource string) *Manager {
	return &Manager{
		Home: home, ManifestSource: manifestSource,
		Runtime: runtimeinstall.NewManager(home), Core: installation.NewManager(home), Desktop: desktopinstall.NewManager(home),
		ProbeHost: windowssetup.Probe, EnableWSL: windowssetup.EnableWSL, RegisterResume: windowssetup.RegisterResume,
		Run: run,
	}
}

// Inspect completes every non-mutating prerequisite and component decision.
func (m *Manager) Inspect(ctx context.Context, manifest releasebundle.Manifest) (Plan, error) {
	host, err := m.ProbeHost(ctx)
	if err != nil {
		return Plan{Version: manifest.Version, Host: host}, err
	}
	runtimePlan, err := m.Runtime.Inspect(manifest)
	if err != nil {
		return Plan{}, err
	}
	corePlan, _, err := m.Core.Inspect(manifest)
	if err != nil {
		return Plan{}, err
	}
	desktopPlan, err := m.Desktop.Inspect(manifest)
	if err != nil {
		return Plan{}, err
	}
	ready := host.Supported && !host.RequiresEnablement && runtimePlan.Ready && corePlan.Ready
	return Plan{
		Version: manifest.Version, Host: host, Runtime: runtimePlan, Core: corePlan, Desktop: desktopPlan,
		DownloadBytes: runtimePlan.DownloadBytes + corePlan.DownloadBytes + desktopPlan.DownloadBytes,
		Ready:         ready,
	}, nil
}

// Install applies the accepted plan in dependency order and runs the existing
// signed bootstrap self-test before exposing the desktop launcher.
func (m *Manager) Install(ctx context.Context, manifest releasebundle.Manifest) error {
	plan, err := m.Inspect(ctx, manifest)
	if err != nil {
		return err
	}
	if plan.Host.RequiresEnablement {
		m.report("windows", "Enabling WSL2 prerequisites")
		reboot, err := m.EnableWSL(ctx)
		if err != nil {
			return err
		}
		if reboot {
			if err := m.RegisterResume(); err != nil {
				return err
			}
			return ErrRebootRequired
		}
	}
	if !plan.Runtime.Ready || !plan.Core.Ready {
		return errors.New("setup plan does not have enough disk space")
	}
	m.report("runtime", "Installing the verified private Podman runtime")
	if err := m.Runtime.Install(ctx, manifest); err != nil {
		return err
	}
	m.report("core", "Installing and verifying the LCTK host core")
	if _, err := m.Core.Install(ctx, manifest); err != nil {
		return err
	}
	executable, _, err := installation.ActiveExecutable(m.Home)
	if err != nil {
		return err
	}
	m.report("components", "Installing container images and the embedding model")
	bootstrapCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	output, err := m.Run(bootstrapCtx, executable, "bootstrap", "--manifest", m.ManifestSource, "--yes", "--json")
	if err != nil {
		return fmt.Errorf("bootstrap signed runtime components: %s: %w", string(output), err)
	}
	m.report("desktop", "Registering LCTK at sign-in and in the Start menu")
	if _, err := m.Desktop.Install(ctx, manifest); err != nil {
		return err
	}
	m.report("complete", "LCTK is installed and ready")
	return nil
}

func (m *Manager) report(phase, detail string) {
	if m.Progress != nil {
		m.Progress(phase, detail)
	}
}

func run(ctx context.Context, executable string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, executable, args...).CombinedOutput()
}
