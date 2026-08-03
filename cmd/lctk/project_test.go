package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/projectmanifest"
	"github.com/lev-goryachev/lctk/internal/projectpath"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

// isolateHome points host state at a temporary directory so the tests never read
// or write the developer's real registry.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(lctkhome.EnvOverride, home)
	return home
}

func makeProjectDir(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(parts...)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// project runs a project subcommand and returns stdout, stderr, and the error.
func project(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runProject(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestProjectAddRegistersAndPersists(t *testing.T) {
	home := isolateHome(t)
	dir := makeProjectDir(t, t.TempDir(), "alpha")

	stdout, _, err := project(t, "add", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Registered ") {
		t.Errorf("output does not confirm registration:\n%s", stdout)
	}
	// The command must say plainly that nothing was started.
	if !strings.Contains(stdout, "No services were started") {
		t.Errorf("output does not state that no services were started:\n%s", stdout)
	}

	if _, err := os.Stat(filepath.Join(home, projectregistry.FileName)); err != nil {
		t.Errorf("registry was not persisted: %v", err)
	}

	registry, err := projectregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 1 {
		t.Fatalf("len = %d, want 1", registry.Len())
	}
}

func TestProjectAddJSONOutput(t *testing.T) {
	isolateHome(t)
	dir := makeProjectDir(t, t.TempDir(), "alpha")

	stdout, _, err := project(t, "add", "--json", dir)
	if err != nil {
		t.Fatal(err)
	}

	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout)
	}
	if view.ID == "" {
		t.Error("id is empty")
	}
	if view.Name != "alpha" {
		t.Errorf("name = %q", view.Name)
	}
	if view.Profile != string(projectregistry.ProfileMinimal) {
		t.Errorf("profile = %q, want the minimal default", view.Profile)
	}
	if !view.PathAvailable {
		t.Error("path should be reported as available")
	}
	if view.ManifestPresent {
		t.Error("manifest reported present when none exists")
	}
}

func TestProjectAddProfilePrecedence(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()

	// The manifest may propose a profile.
	proposed := makeProjectDir(t, root, "proposed")
	if err := os.WriteFile(filepath.Join(proposed, projectmanifest.FileName),
		[]byte("profile: full\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := project(t, "add", "--json", proposed)
	if err != nil {
		t.Fatal(err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.Profile != "full" {
		t.Errorf("manifest proposal was ignored: profile = %q", view.Profile)
	}
	if !view.ManifestPresent {
		t.Error("manifest presence was not recorded")
	}

	// An explicit flag must override the manifest.
	overridden := makeProjectDir(t, root, "overridden")
	if err := os.WriteFile(filepath.Join(overridden, projectmanifest.FileName),
		[]byte("profile: full\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = project(t, "add", "--json", "--profile", "minimal", overridden)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.Profile != "minimal" {
		t.Errorf("the flag did not win: profile = %q", view.Profile)
	}
}

// TestProjectAddIgnoresAManifestHostPath is the roadmap's required check at the
// command boundary: a manifest cannot replace the authoritative host path.
func TestProjectAddIgnoresAManifestHostPath(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	real := makeProjectDir(t, root, "real")
	decoy := makeProjectDir(t, root, "decoy")

	body := "profile: minimal\nhost_path: " + filepath.ToSlash(decoy) + "\n"
	if err := os.WriteFile(filepath.Join(real, projectmanifest.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// A manifest that attempts it is rejected outright rather than partially applied.
	_, _, err := project(t, "add", real)
	if !errors.Is(err, projectmanifest.ErrForbiddenField) {
		t.Fatalf("got %v, want ErrForbiddenField", err)
	}

	registry, err := projectregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 0 {
		t.Fatalf("a rejected manifest still produced a registration: %+v", registry.List())
	}

	// With the offending field removed, registration binds the folder that was
	// named on the command line, never the decoy.
	if err := os.WriteFile(filepath.Join(real, projectmanifest.FileName), []byte("profile: minimal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := project(t, "add", "--json", real)
	if err != nil {
		t.Fatal(err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	// Compare against the canonical form of the folder given on the command line:
	// the temporary directory may be handed to the test in an alias form, such as
	// a Windows 8.3 short name, which registration deliberately expands.
	expected, err := projectpath.Resolve(real)
	if err != nil {
		t.Fatal(err)
	}
	if view.Path != expected.Display {
		t.Errorf("registered path = %q, want the canonical form of the command-line folder %q",
			view.Path, expected.Display)
	}
	if strings.Contains(strings.ToLower(view.Path), "decoy") {
		t.Errorf("the manifest redirected the host path to %q", view.Path)
	}
}

func TestProjectAddRejectsDuplicatesAndMissingPaths(t *testing.T) {
	isolateHome(t)
	dir := makeProjectDir(t, t.TempDir(), "alpha")

	if _, _, err := project(t, "add", dir); err != nil {
		t.Fatal(err)
	}
	_, _, err := project(t, "add", dir)
	if !errors.Is(err, projectregistry.ErrAlreadyRegistered) {
		t.Errorf("got %v, want ErrAlreadyRegistered", err)
	}

	if _, _, err := project(t, "add", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing path was accepted")
	}
	if _, _, err := project(t, "add"); err == nil {
		t.Error("add without a path was accepted")
	}
	if _, _, err := project(t, "add", "--profile", "godmode", dir); err == nil {
		t.Error("an unknown profile was accepted")
	}
}

func TestProjectStatusListsAndReportsOne(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()

	// Empty state must be explained rather than printing nothing.
	stdout, _, err := project(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "No projects are registered") {
		t.Errorf("empty status output:\n%s", stdout)
	}

	alpha := makeProjectDir(t, root, "alpha")
	beta := makeProjectDir(t, root, "beta")
	for _, dir := range []string{alpha, beta} {
		if _, _, err := project(t, "add", dir); err != nil {
			t.Fatal(err)
		}
	}

	stdout, _, err = project(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "NAME", "PROFILE", "PATH", "alpha", "beta"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status listing is missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = project(t, "status", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "name:") || !strings.Contains(stdout, "alpha") {
		t.Errorf("single-project status:\n%s", stdout)
	}

	// JSON listing must be an array, and a single project an object.
	stdout, _, err = project(t, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var list []projectView
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("listing is not a JSON array: %v\n%s", err, stdout)
	}
	if len(list) != 2 {
		t.Errorf("listing has %d entries, want 2", len(list))
	}

	stdout, _, err = project(t, "status", "--json", "beta")
	if err != nil {
		t.Fatal(err)
	}
	var one projectView
	if err := json.Unmarshal([]byte(stdout), &one); err != nil {
		t.Fatalf("single status is not a JSON object: %v\n%s", err, stdout)
	}
	if one.Name != "beta" {
		t.Errorf("name = %q, want beta", one.Name)
	}
}

func TestProjectStatusReportsAnUnavailablePath(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	dir := makeProjectDir(t, root, "vanishing")

	if _, _, err := project(t, "add", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := project(t, "status", "--json", "vanishing")
	if err != nil {
		t.Fatalf("status must still describe a project whose folder is gone: %v", err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.PathAvailable {
		t.Error("a removed folder is reported as available")
	}
}

func TestProjectStatusReflectsCurrentManifest(t *testing.T) {
	isolateHome(t)
	dir := makeProjectDir(t, t.TempDir(), "alpha")

	if _, _, err := project(t, "add", dir); err != nil {
		t.Fatal(err)
	}

	// A manifest added after registration must show up, because status re-reads it.
	if err := os.WriteFile(filepath.Join(dir, projectmanifest.FileName),
		[]byte("profile: full\nmystery: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := project(t, "status", "--json", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var view projectView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if !view.ManifestPresent {
		t.Error("a manifest added after registration was not detected")
	}
	if len(view.Warnings) == 0 {
		t.Error("an unknown manifest field did not produce a warning")
	}

	// And the listing has to say the same thing. Asking about every project is not
	// a weaker question than asking about one of them, and two answers to it means
	// whichever the reader happened to run decides what they believe.
	listed, _, err := project(t, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var views []projectView
	if err := json.Unmarshal([]byte(listed), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("the listing reported %d projects, want 1", len(views))
	}
	if views[0].ManifestPresent != view.ManifestPresent {
		t.Errorf("the listing says manifest_present=%v and the single project says %v",
			views[0].ManifestPresent, view.ManifestPresent)
	}
}

func TestProjectRemove(t *testing.T) {
	isolateHome(t)
	dir := makeProjectDir(t, t.TempDir(), "alpha")

	if _, _, err := project(t, "add", dir); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := project(t, "remove", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Removed ") {
		t.Errorf("output does not confirm removal:\n%s", stdout)
	}
	if !strings.Contains(stdout, "was not deleted") {
		t.Errorf("output does not state that data was preserved:\n%s", stdout)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("remove deleted the project folder: %v", err)
	}

	registry, err := projectregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 0 {
		t.Errorf("len = %d, want 0", registry.Len())
	}

	if _, _, err := project(t, "remove", "alpha"); !errors.Is(err, projectregistry.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
	if _, _, err := project(t, "remove"); err == nil {
		t.Error("remove without a reference was accepted")
	}
}

func TestProjectAddAcceptsAnAliasOnlyOnce(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	target := makeProjectDir(t, root, "real")
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink in this environment: %v", err)
	}

	if _, _, err := project(t, "add", target); err != nil {
		t.Fatal(err)
	}
	if _, _, err := project(t, "add", link); !errors.Is(err, projectregistry.ErrAlreadyRegistered) {
		t.Errorf("a symlink alias registered a second project: %v", err)
	}

	// Both spellings must resolve to the one registration.
	for _, reference := range []string{target, link} {
		if _, _, err := project(t, "status", reference); err != nil {
			t.Errorf("status %q failed: %v", reference, err)
		}
	}
}

func TestProjectUsageErrors(t *testing.T) {
	isolateHome(t)
	if _, stderr, err := project(t); err == nil {
		t.Error("an empty subcommand was accepted")
	} else if !strings.Contains(stderr, "lctk project add") {
		t.Errorf("usage was not printed:\n%s", stderr)
	}
	if _, _, err := project(t, "frobnicate"); err == nil {
		t.Error("an unknown subcommand was accepted")
	}
	stdout, _, err := project(t, "help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "lctk project status") {
		t.Errorf("help output:\n%s", stdout)
	}
}

func TestProjectCommandIsReachableFromRun(t *testing.T) {
	isolateHome(t)
	dir := makeProjectDir(t, t.TempDir(), "alpha")

	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"project", "add", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Registered ") {
		t.Errorf("project is not wired into the top-level command:\n%s", stdout.String())
	}
}
