package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/lev-goryachev/lctk/internal/inference"
	"github.com/lev-goryachev/lctk/internal/installation"
	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
	"github.com/lev-goryachev/lctk/internal/projectstack"
	"github.com/lev-goryachev/lctk/internal/releasebundle"
)

const currentProjectSchema = 2

type updateProject struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
}

type updatePlan struct {
	CurrentVersion string            `json:"current_version"`
	TargetVersion  string            `json:"target_version"`
	ManifestSource string            `json:"manifest_source"`
	SignatureValid bool              `json:"signature_valid"`
	Writes         bool              `json:"writes"`
	Ready          bool              `json:"ready"`
	Host           installation.Plan `json:"host"`
	Projects       []updateProject   `json:"projects"`
	Applied        bool              `json:"applied"`
	RolledBack     bool              `json:"rolled_back"`
}

type updateStack interface {
	RuntimeAvailable(context.Context) error
	InstallImage(context.Context, string, string) error
	Status(context.Context, projectregistry.Project) (projectstack.Status, error)
	Start(context.Context, projectregistry.Project, time.Duration) (projectstack.Status, error)
	Stop(context.Context, projectregistry.Project) (projectstack.Status, error)
	RestoreSchemaRollback(context.Context, projectregistry.Project, string) error
}

type updateInstaller interface {
	Inspect(releasebundle.Manifest) (installation.Plan, releasebundle.Artifact, error)
	Install(context.Context, releasebundle.Manifest) (installation.Activation, error)
	VerifyRollback() (installation.Activation, error)
	Rollback() (installation.Activation, error)
}

var (
	newUpdateVerifier  = releasebundle.ProductionVerifier
	loadUpdateManifest = func(ctx context.Context, source string, verifier releasebundle.Verifier) (releasebundle.Manifest, error) {
		return releasebundle.Load(ctx, source, http.DefaultClient, verifier)
	}
	newUpdateStack = func(version string) updateStack {
		return projectstack.NewManager().WithVersion(version)
	}
	newUpdateInstaller = func(home string) updateInstaller {
		return installation.NewManager(home)
	}
	loadUpdateRegistry = projectregistry.Load
)

func runUpdate(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "rollback" {
		return runUpdateRollback(ctx, args[1:], stdout)
	}
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("manifest", "", "signed release manifest HTTPS URL or local file")
	planOnly := flags.Bool("plan", false, "perform read-only update preflight")
	proceed := flags.Bool("yes", false, "apply the verified update plan")
	asJSON := flags.Bool("json", false, "write machine-readable plan and result")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: lctk update [--manifest SOURCE] [--plan] [--yes] [--json]")
	}

	verifier, err := newUpdateVerifier()
	if err != nil {
		return err
	}
	manifest, err := loadUpdateManifest(ctx, *source, verifier)
	if err != nil {
		return err
	}
	if !releasebundle.VersionAtLeast(buildinfo.Version, manifest.MinimumHostVersion) {
		return fmt.Errorf("release %s requires host %s or newer", manifest.Version, manifest.MinimumHostVersion)
	}
	if manifest.Version == buildinfo.Version || !releasebundle.VersionAtLeast(manifest.Version, buildinfo.Version) {
		return fmt.Errorf("release %s is not newer than active host %s; use explicit rollback for downgrades", manifest.Version, buildinfo.Version)
	}
	if currentProjectSchema < manifest.ProjectSchemaFrom || currentProjectSchema > manifest.ProjectSchemaTo {
		return fmt.Errorf("release %s does not accept project schema %d", manifest.Version, currentProjectSchema)
	}
	if manifest.InferenceImage.Reference != inference.Image ||
		manifest.EmbeddingModel.SHA256 != inference.ModelSHA256 ||
		manifest.EmbeddingModel.Bytes != inference.ModelBytes {
		return errors.New("release manifest inference or model identity differs from this compatibility generation")
	}

	home, err := lctkhome.Dir()
	if err != nil {
		return err
	}
	installer := newUpdateInstaller(home)
	hostPlan, _, err := installer.Inspect(manifest)
	if err != nil {
		return err
	}
	registry, err := loadUpdateRegistry()
	if err != nil {
		return err
	}
	currentStack := newUpdateStack(buildinfo.Version)
	preflightCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := currentStack.RuntimeAvailable(preflightCtx); err != nil {
		return err
	}
	projects, running, err := inventoryProjects(preflightCtx, currentStack, registry.List())
	if err != nil {
		return err
	}
	plan := updatePlan{
		CurrentVersion: buildinfo.Version, TargetVersion: manifest.Version,
		ManifestSource: *source, SignatureValid: true, Writes: false,
		Ready: hostPlan.Ready, Host: hostPlan, Projects: projects,
	}
	if *planOnly || !*proceed {
		if *asJSON {
			return writeJSON(stdout, plan)
		}
		fmt.Fprintf(stdout, "Verified update %s -> %s; %d registered projects, %d running; writes: false\n",
			plan.CurrentVersion, plan.TargetVersion, len(projects), len(running))
		if !*planOnly {
			fmt.Fprintln(stdout, "No changes applied. Review the plan, then run lctk update --yes.")
		}
		return nil
	}
	if !hostPlan.Ready {
		return fmt.Errorf("update requires %d bytes; only %d are available", hostPlan.RequiredBytes, hostPlan.AvailableBytes)
	}

	applyCtx, applyCancel := context.WithTimeout(ctx, 30*time.Minute)
	defer applyCancel()
	targetStack := newUpdateStack(manifest.Version)
	if err := targetStack.InstallImage(applyCtx, manifest.CodeImage.Reference, manifest.Version); err != nil {
		return err
	}
	migrated, err := switchRunningProjects(applyCtx, currentStack, targetStack, running)
	if err != nil {
		rollbackErr := restoreProjects(applyCtx, targetStack, currentStack, migrated, buildinfo.Version)
		return errors.Join(err, rollbackErr)
	}
	if _, err := installer.Install(applyCtx, manifest); err != nil {
		rollbackErr := restoreProjects(applyCtx, targetStack, currentStack, migrated, buildinfo.Version)
		return errors.Join(err, rollbackErr)
	}
	plan.Writes = true
	plan.Applied = true
	plan.Ready = true
	if *asJSON {
		return writeJSON(stdout, plan)
	}
	fmt.Fprintf(stdout, "Updated to %s; every previously running project passed its health gate.\n", manifest.Version)
	return nil
}

func inventoryProjects(ctx context.Context, stack updateStack, projects []projectregistry.Project) ([]updateProject, []projectregistry.Project, error) {
	result := make([]updateProject, 0, len(projects))
	running := make([]projectregistry.Project, 0, len(projects))
	for _, project := range projects {
		status, err := stack.Status(ctx, project)
		if err != nil {
			return nil, nil, fmt.Errorf("inspect project %s before update: %w", project.ID, err)
		}
		isRunning := status.State == projectstack.StateRunning
		result = append(result, updateProject{ID: project.ID, Running: isRunning})
		if isRunning {
			running = append(running, project)
		}
	}
	return result, running, nil
}

func switchRunningProjects(ctx context.Context, current, target updateStack, projects []projectregistry.Project) ([]projectregistry.Project, error) {
	migrated := make([]projectregistry.Project, 0, len(projects))
	for _, project := range projects {
		if _, err := current.Stop(ctx, project); err != nil {
			return migrated, fmt.Errorf("stop project %s for update: %w", project.ID, err)
		}
		migrated = append(migrated, project)
		if _, err := target.Start(ctx, project, defaultStartWait); err != nil {
			return migrated, fmt.Errorf("candidate health gate failed for project %s: %w", project.ID, err)
		}
	}
	return migrated, nil
}

func restoreProjects(ctx context.Context, candidate, previous updateStack, projects []projectregistry.Project, previousVersion string) error {
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
		if _, err := previous.Start(ctx, project, defaultStartWait); err != nil {
			failures = append(failures, fmt.Errorf("restore project %s: %w", project.ID, err))
		}
	}
	return errors.Join(failures...)
}

func runUpdateRollback(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("update rollback", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write machine-readable rollback result")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: lctk update rollback [--json]")
	}
	home, err := lctkhome.Dir()
	if err != nil {
		return err
	}
	installer := newUpdateInstaller(home)
	current, err := installer.VerifyRollback()
	if err != nil {
		return err
	}
	if current.PreviousVersion == "" {
		return errors.New("no previous LCTK release is available")
	}
	registry, err := loadUpdateRegistry()
	if err != nil {
		return err
	}
	currentStack := newUpdateStack(current.ActiveVersion)
	previousStack := newUpdateStack(current.PreviousVersion)
	projects := registry.List()
	_, running, err := inventoryProjects(ctx, currentStack, projects)
	if err != nil {
		return err
	}
	if err := rollbackRegisteredProjects(ctx, currentStack, previousStack, projects, running, current.PreviousVersion); err != nil {
		return err
	}
	activation, err := installer.Rollback()
	if err != nil {
		return err
	}
	result := updatePlan{CurrentVersion: current.ActiveVersion, TargetVersion: activation.ActiveVersion,
		Writes: true, Ready: true, Applied: true, RolledBack: true}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Rolled back to %s.\n", activation.ActiveVersion)
	return nil
}

func rollbackRegisteredProjects(ctx context.Context, current, previous updateStack,
	projects, running []projectregistry.Project, previousVersion string) error {
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
		if !runningIDs[project.ID] {
			continue
		}
		if _, err := previous.Start(ctx, project, defaultStartWait); err != nil {
			failures = append(failures, fmt.Errorf("restart project %s after rollback: %w", project.ID, err))
		}
	}
	return errors.Join(failures...)
}
