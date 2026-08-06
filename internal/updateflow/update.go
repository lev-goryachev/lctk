// Package updateflow owns the signed in-place update transaction shared by the
// CLI and the native Windows setup. Keeping one coordinator prevents the GUI
// installer from bypassing project health gates or host-core rollback.
package updateflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lev-goryachev/lctk/internal/inference"
	"github.com/lev-goryachev/lctk/internal/installation"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
	"github.com/lev-goryachev/lctk/internal/projectstack"
	"github.com/lev-goryachev/lctk/internal/releasebundle"
)

const (
	// CurrentProjectSchema is the persistent project schema understood by this
	// host generation and checked before any candidate component is installed.
	CurrentProjectSchema = 2
	// DefaultStartWait is the bounded candidate health gate used for every
	// project that was running immediately before an update or rollback.
	DefaultStartWait = 90 * time.Second
)

// Project records the only lifecycle fact an update needs to preserve.
type Project struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
}

// Plan is read-only evidence for one complete signed update transaction.
type Plan struct {
	CurrentVersion string            `json:"current_version"`
	TargetVersion  string            `json:"target_version"`
	ManifestSource string            `json:"manifest_source"`
	SignatureValid bool              `json:"signature_valid"`
	Writes         bool              `json:"writes"`
	Ready          bool              `json:"ready"`
	Host           installation.Plan `json:"host"`
	Projects       []Project         `json:"projects"`
	Applied        bool              `json:"applied"`
	RolledBack     bool              `json:"rolled_back"`
}

// Stack is the version-pinned project runtime boundary used during update.
type Stack interface {
	RuntimeAvailable(context.Context) error
	InstallImage(context.Context, string, string) error
	Status(context.Context, projectregistry.Project) (projectstack.Status, error)
	Start(context.Context, projectregistry.Project, time.Duration) (projectstack.Status, error)
	Stop(context.Context, projectregistry.Project) (projectstack.Status, error)
	RestoreSchemaRollback(context.Context, projectregistry.Project, string) error
}

// Installer is the atomic versioned host-core activation boundary.
type Installer interface {
	Inspect(releasebundle.Manifest) (installation.Plan, releasebundle.Artifact, error)
	Install(context.Context, releasebundle.Manifest) (installation.Activation, error)
	VerifyRollback() (installation.Activation, error)
	Rollback() (installation.Activation, error)
}

// Manager injects every mutable boundary so the same transaction can serve
// the CLI, native setup, and deterministic unit tests.
type Manager struct {
	Home           string
	CurrentVersion string
	ManifestSource string
	Installer      Installer
	LoadRegistry   func() (*projectregistry.Registry, error)
	NewStack       func(string) Stack
	ProjectSchema  int
	CandidateWait  time.Duration
}

// NewManager returns the production update coordinator for an already verified
// manifest. Signature loading remains with the calling CLI or setup surface.
func NewManager(home, currentVersion, manifestSource string) *Manager {
	return &Manager{
		Home: home, CurrentVersion: currentVersion, ManifestSource: manifestSource,
		Installer: installation.NewManager(home), LoadRegistry: projectregistry.Load,
		NewStack:      func(version string) Stack { return projectstack.NewManager().WithVersion(version) },
		ProjectSchema: CurrentProjectSchema, CandidateWait: DefaultStartWait,
	}
}

// Inspect validates compatibility, inventories projects, and calculates host
// disk requirements without writing state or pulling a candidate image.
func (m *Manager) Inspect(ctx context.Context, manifest releasebundle.Manifest) (Plan, error) {
	if err := m.validate(manifest); err != nil {
		return Plan{}, err
	}
	hostPlan, _, err := m.Installer.Inspect(manifest)
	if err != nil {
		return Plan{}, err
	}
	registry, err := m.LoadRegistry()
	if err != nil {
		return Plan{}, err
	}
	currentStack := m.NewStack(m.CurrentVersion)
	if err := currentStack.RuntimeAvailable(ctx); err != nil {
		return Plan{}, err
	}
	projects, _, err := InventoryProjects(ctx, currentStack, registry.List())
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		CurrentVersion: m.CurrentVersion, TargetVersion: manifest.Version,
		ManifestSource: m.ManifestSource, SignatureValid: true, Writes: false,
		Ready: hostPlan.Ready, Host: hostPlan, Projects: projects,
	}, nil
}

// Apply repeats the read-only gates, migrates only previously running projects,
// and activates the verified host core last. Every migrated project is restored
// in reverse order when a candidate health gate or host activation fails.
func (m *Manager) Apply(ctx context.Context, manifest releasebundle.Manifest) (Plan, error) {
	plan, err := m.Inspect(ctx, manifest)
	if err != nil {
		return Plan{}, err
	}
	if !plan.Ready {
		return plan, fmt.Errorf("update requires %d bytes; only %d are available", plan.Host.RequiredBytes, plan.Host.AvailableBytes)
	}
	registry, err := m.LoadRegistry()
	if err != nil {
		return plan, err
	}
	currentStack := m.NewStack(m.CurrentVersion)
	_, running, err := InventoryProjects(ctx, currentStack, registry.List())
	if err != nil {
		return plan, err
	}
	targetStack := m.NewStack(manifest.Version)
	if err := targetStack.InstallImage(ctx, manifest.CodeImage.Reference, manifest.Version); err != nil {
		return plan, err
	}
	migrated, err := SwitchRunningProjects(ctx, currentStack, targetStack, running, m.CandidateWait)
	if err != nil {
		return plan, errors.Join(err, RestoreProjects(ctx, targetStack, currentStack, migrated, m.CurrentVersion, m.CandidateWait))
	}
	if _, err := m.Installer.Install(ctx, manifest); err != nil {
		return plan, errors.Join(err, RestoreProjects(ctx, targetStack, currentStack, migrated, m.CurrentVersion, m.CandidateWait))
	}
	plan.Writes, plan.Applied, plan.Ready = true, true, true
	return plan, nil
}

// Rollback verifies the previous host before touching project state, restores
// all registered schema bundles, restarts only previously running projects,
// and changes host activation last.
func (m *Manager) Rollback(ctx context.Context) (Plan, error) {
	current, err := m.Installer.VerifyRollback()
	if err != nil {
		return Plan{}, err
	}
	registry, err := m.LoadRegistry()
	if err != nil {
		return Plan{}, err
	}
	currentStack := m.NewStack(current.ActiveVersion)
	previousStack := m.NewStack(current.PreviousVersion)
	projects := registry.List()
	_, running, err := InventoryProjects(ctx, currentStack, projects)
	if err != nil {
		return Plan{}, err
	}
	if err := RollbackRegisteredProjects(ctx, currentStack, previousStack, projects, running, current.PreviousVersion, m.CandidateWait); err != nil {
		return Plan{}, err
	}
	activation, err := m.Installer.Rollback()
	if err != nil {
		return Plan{}, err
	}
	return Plan{CurrentVersion: current.ActiveVersion, TargetVersion: activation.ActiveVersion,
		Writes: true, Ready: true, Applied: true, RolledBack: true}, nil
}

// validate rejects stale, incompatible, or incomplete coordinator inputs before
// any runtime inventory can reach Podman.
func (m *Manager) validate(manifest releasebundle.Manifest) error {
	if m.Home == "" || m.CurrentVersion == "" || m.Installer == nil || m.LoadRegistry == nil || m.NewStack == nil || m.ProjectSchema < 1 || m.CandidateWait <= 0 {
		return errors.New("update coordinator is incomplete")
	}
	if !releasebundle.VersionAtLeast(m.CurrentVersion, manifest.MinimumHostVersion) {
		return fmt.Errorf("release %s requires host %s or newer", manifest.Version, manifest.MinimumHostVersion)
	}
	if manifest.Version == m.CurrentVersion || !releasebundle.VersionAtLeast(manifest.Version, m.CurrentVersion) {
		return fmt.Errorf("release %s is not newer than active host %s; use explicit rollback for downgrades", manifest.Version, m.CurrentVersion)
	}
	if m.ProjectSchema < manifest.ProjectSchemaFrom || m.ProjectSchema > manifest.ProjectSchemaTo {
		return fmt.Errorf("release %s does not accept project schema %d", manifest.Version, m.ProjectSchema)
	}
	if manifest.InferenceImage.Reference != inference.Image || manifest.EmbeddingModel.SHA256 != inference.ModelSHA256 || manifest.EmbeddingModel.Bytes != inference.ModelBytes {
		return errors.New("release manifest inference or model identity differs from this compatibility generation")
	}
	return nil
}

// InventoryProjects records which registered projects are running without
// starting or stopping any project.
func InventoryProjects(ctx context.Context, stack Stack, projects []projectregistry.Project) ([]Project, []projectregistry.Project, error) {
	result := make([]Project, 0, len(projects))
	running := make([]projectregistry.Project, 0, len(projects))
	for _, project := range projects {
		status, err := stack.Status(ctx, project)
		if err != nil {
			return nil, nil, fmt.Errorf("inspect project %s before update: %w", project.ID, err)
		}
		isRunning := status.State == projectstack.StateRunning
		result = append(result, Project{ID: project.ID, Running: isRunning})
		if isRunning {
			running = append(running, project)
		}
	}
	return result, running, nil
}

// SwitchRunningProjects stops each old stack and accepts the candidate only
// after its bounded health gate succeeds.
func SwitchRunningProjects(ctx context.Context, current, target Stack, projects []projectregistry.Project, wait time.Duration) ([]projectregistry.Project, error) {
	migrated := make([]projectregistry.Project, 0, len(projects))
	for _, project := range projects {
		if _, err := current.Stop(ctx, project); err != nil {
			return migrated, fmt.Errorf("stop project %s for update: %w", project.ID, err)
		}
		migrated = append(migrated, project)
		if _, err := target.Start(ctx, project, wait); err != nil {
			return migrated, fmt.Errorf("candidate health gate failed for project %s: %w", project.ID, err)
		}
	}
	return migrated, nil
}

// RestoreProjects reverses a failed candidate migration and retains every
// individual failure so recovery never silently stops halfway.
func RestoreProjects(ctx context.Context, candidate, previous Stack, projects []projectregistry.Project, previousVersion string, wait time.Duration) error {
	var failures []error
	for index := len(projects) - 1; index >= 0; index-- {
		project := projects[index]
		if _, err := candidate.Stop(ctx, project); err != nil {
			failures = append(failures, fmt.Errorf("stop failed candidate %s: %w", project.ID, err))
			continue
		}
		if err := candidate.RestoreSchemaRollback(ctx, project, previousVersion); err != nil {
			failures = append(failures, err)
			continue
		}
		if _, err := previous.Start(ctx, project, wait); err != nil {
			failures = append(failures, fmt.Errorf("restore project %s: %w", project.ID, err))
		}
	}
	return errors.Join(failures...)
}

// RollbackRegisteredProjects restores every registered schema bundle but starts
// only the projects that were running before rollback began.
func RollbackRegisteredProjects(ctx context.Context, current, previous Stack, projects, running []projectregistry.Project, previousVersion string, wait time.Duration) error {
	runningIDs := make(map[string]bool, len(running))
	var failures []error
	for _, project := range running {
		runningIDs[project.ID] = true
		if _, err := current.Stop(ctx, project); err != nil {
			failures = append(failures, fmt.Errorf("stop project %s before rollback: %w", project.ID, err))
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	for _, project := range projects {
		if err := current.RestoreSchemaRollback(ctx, project, previousVersion); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	for _, project := range projects {
		if runningIDs[project.ID] {
			if _, err := previous.Start(ctx, project, wait); err != nil {
				failures = append(failures, fmt.Errorf("restart project %s after rollback: %w", project.ID, err))
			}
		}
	}
	return errors.Join(failures...)
}
