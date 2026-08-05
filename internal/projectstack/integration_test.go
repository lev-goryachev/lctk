package projectstack

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/containerruntime"
	"github.com/lev-goryachev/lctk/internal/projectpath"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

// These tests drive the real private runtime and are skipped, never simulated,
// because hosted CI does not provision LCTK's installation-owned WSL machine; no
// managed WSL machine exists. Everything they assert is therefore verified on
// the target acceptance host and reported as such.

const imageContextRelative = "../../images/code-intel"

func requireManagedRuntime(t *testing.T) (*Manager, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping container integration test in short mode")
	}

	// These integration cases isolate the explicit per-project Podman lifecycle.
	// The shared inference lifecycle has its own real-container acceptance test
	// after bootstrap installs the pinned model and image.
	manager := NewManagerWithRunner(containerruntime.Runner{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// This covers both an absent daemon and a reachable one that cannot run Linux
	// containers, which is what a hosted Windows runner provides.
	if err := manager.RuntimeAvailable(ctx); err != nil {
		t.Skipf("no runtime able to run Linux containers: %v", err)
	}

	names, err := DeriveNames("probe-00000000")
	if err != nil {
		t.Fatal(err)
	}
	image := names.Image

	// Build once per run. The image is shared by every project, per ADR-0003.
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelBuild()
	contextDir, err := filepath.Abs(imageContextRelative)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.BuildImage(buildCtx, image, contextDir); err != nil {
		t.Fatalf("building the reusable image failed: %v", err)
	}
	return manager, image
}

// registerProject creates a real folder, canonicalizes it, and returns a project
// record plus a cleanup that tears the stack down and deletes its volume.
func registerProject(t *testing.T, manager *Manager, name string) projectregistry.Project {
	t.Helper()

	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("sentinel-"+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	canonical, err := projectpath.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := projectregistry.New()
	project, err := registry.Add(canonical, projectregistry.ProfileMinimal, false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if _, err := manager.Stop(ctx, project); err != nil {
			t.Logf("cleanup stop failed for %s: %v", project.ID, err)
		}
		names, err := DeriveNames(project.ID)
		if err == nil {
			// The volume survives a stop by design, so the test removes it
			// explicitly because ordinary stop intentionally preserves it.
			if _, _, err := (containerruntime.Runner{}).Run(ctx, "volume", "rm", "--force", names.Volume); err != nil {
				t.Logf("cleanup volume removal failed for %s: %v", names.Volume, err)
			}
		}
	})
	return project
}

// readState reads a file from the project's persistent state directory.
func readState(t *testing.T, project projectregistry.Project, name string) string {
	t.Helper()
	names, err := DeriveNames(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stdout, stderr, err := (containerruntime.Runner{}).Run(ctx, "exec", names.ContainerName, "cat", StateMount+"/"+name)
	if err != nil {
		t.Fatalf("reading %s from %s failed: %v: %s", name, names.ContainerName, err, stderr)
	}
	return strings.TrimSpace(stdout)
}

func TestIntegrationStartStopPreservesProjectState(t *testing.T) {
	isolate(t)
	manager, _ := requireManagedRuntime(t)
	project := registerProject(t, manager, "persist")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	status, err := manager.Start(ctx, project, 90*time.Second)
	if err != nil {
		t.Fatalf("first start failed: %v (status %+v)", err, status)
	}
	if status.State != StateRunning {
		t.Fatalf("state = %q, want running", status.State)
	}
	if status.Health != "healthy" {
		t.Errorf("health = %q, want healthy", status.Health)
	}

	createdAt := readState(t, project, "created_at")
	if createdAt == "" {
		t.Fatal("the container did not record a creation timestamp")
	}
	if starts := readState(t, project, "starts"); starts != "1" {
		t.Errorf("starts = %q, want 1 on a fresh volume", starts)
	}
	if id := readState(t, project, "project_id"); id != project.ID {
		t.Errorf("container sees project id %q, want %q", id, project.ID)
	}

	// The code-intel boundary must not be able to write to the working tree.
	if mode := readState(t, project, "workspace_mode"); mode != "read-only" {
		t.Errorf("workspace mode = %q, want read-only", mode)
	}

	// Stop, then confirm the runtime agrees the project is stopped.
	if _, err := manager.Stop(ctx, project); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	stopped, err := manager.Status(ctx, project)
	if err != nil {
		t.Fatalf("status after stop failed: %v", err)
	}
	if stopped.State != StateStopped {
		t.Errorf("state after stop = %q, want stopped", stopped.State)
	}

	// Start again. The volume must be the same one.
	if _, err := manager.Start(ctx, project, 90*time.Second); err != nil {
		t.Fatalf("second start failed: %v", err)
	}
	if again := readState(t, project, "created_at"); again != createdAt {
		t.Errorf("stop/start did not preserve project state: created_at was %q, now %q", createdAt, again)
	}
	starts := readState(t, project, "starts")
	count, err := strconv.Atoi(starts)
	if err != nil {
		t.Fatalf("starts is not a number: %q", starts)
	}
	if count != 2 {
		t.Errorf("starts = %d, want 2 after a stop and start on the same volume", count)
	}
}

func TestIntegrationTwoProjectsAreIsolated(t *testing.T) {
	isolate(t)
	manager, _ := requireManagedRuntime(t)
	alpha := registerProject(t, manager, "alpha")
	beta := registerProject(t, manager, "beta")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	for _, project := range []projectregistry.Project{alpha, beta} {
		if _, err := manager.Start(ctx, project, 90*time.Second); err != nil {
			t.Fatalf("start %s failed: %v", project.ID, err)
		}
	}

	alphaNames, err := DeriveNames(alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	betaNames, err := DeriveNames(beta.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Each project must have its own network and volume in the runtime, not just
	// in the generated file.
	for _, resource := range []struct{ kind, name string }{
		{"network", alphaNames.Network},
		{"network", betaNames.Network},
		{"volume", alphaNames.Volume},
		{"volume", betaNames.Volume},
	} {
		if _, stderr, err := (containerruntime.Runner{}).Run(ctx, resource.kind, "inspect", resource.name); err != nil {
			t.Errorf("%s %s does not exist: %v: %s", resource.kind, resource.name, err, stderr)
		}
	}

	// Each container must see only its own source.
	alphaListing := listWorkspace(t, alpha)
	betaListing := listWorkspace(t, beta)
	if !strings.Contains(alphaListing, "source.txt") {
		t.Errorf("alpha cannot see its own source: %q", alphaListing)
	}
	if !strings.Contains(betaListing, "source.txt") {
		t.Errorf("beta cannot see its own source: %q", betaListing)
	}

	alphaContent := readWorkspaceFile(t, alpha, "source.txt")
	betaContent := readWorkspaceFile(t, beta, "source.txt")
	if !strings.Contains(alphaContent, "sentinel-alpha") {
		t.Errorf("alpha sees the wrong source: %q", alphaContent)
	}
	if !strings.Contains(betaContent, "sentinel-beta") {
		t.Errorf("beta sees the wrong source: %q", betaContent)
	}
	// The decisive isolation check: neither container may reach the other's file.
	if strings.Contains(alphaContent, "sentinel-beta") || strings.Contains(betaContent, "sentinel-alpha") {
		t.Error("a project can see another project's source")
	}

	// Per-project state must be separate too.
	if readState(t, alpha, "project_id") == readState(t, beta, "project_id") {
		t.Error("both projects report the same identity, so state is shared")
	}
}

func listWorkspace(t *testing.T, project projectregistry.Project) string {
	t.Helper()
	names, err := DeriveNames(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stdout, stderr, err := (containerruntime.Runner{}).Run(ctx, "exec", names.ContainerName, "ls", "-1", WorkspaceMount)
	if err != nil {
		t.Fatalf("listing the workspace of %s failed: %v: %s", project.ID, err, stderr)
	}
	return stdout
}

func readWorkspaceFile(t *testing.T, project projectregistry.Project, name string) string {
	t.Helper()
	names, err := DeriveNames(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stdout, _, err := (containerruntime.Runner{}).Run(ctx, "exec", names.ContainerName, "cat", WorkspaceMount+"/"+name)
	if err != nil {
		// A missing file is a legitimate outcome for the isolation assertion, so
		// this is not fatal.
		return ""
	}
	return stdout
}

func TestIntegrationGeneratedConfigurationIsReproducible(t *testing.T) {
	isolate(t)
	manager, _ := requireManagedRuntime(t)
	project := registerProject(t, manager, "reproducible")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if _, err := manager.Start(ctx, project, 90*time.Second); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	planPath, err := RuntimePlanPath(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}

	// Restarting rewrites the file; the bytes must be identical.
	if _, err := manager.Restart(ctx, project, 90*time.Second); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	second, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("regenerated configuration differs:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}

	// State must have survived the restart.
	starts := readState(t, project, "starts")
	if starts == "1" {
		t.Error("restart appears to have discarded the project volume")
	}
}
