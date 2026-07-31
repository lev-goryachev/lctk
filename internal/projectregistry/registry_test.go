package projectregistry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/projectpath"
)

// isolate points the LCTK home at a temporary directory so no test touches real
// user state.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(lctkhome.EnvOverride, home)
	return home
}

// makeDir creates a directory and returns its canonical form.
func makeDir(t *testing.T, parts ...string) projectpath.Canonical {
	t.Helper()
	path := filepath.Join(parts...)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := projectpath.Resolve(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestAddAssignsIdentityAndDefaults(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	canonical := makeDir(t, root, "alpha")

	registry := New()
	project, err := registry.Add(canonical, "", true)
	if err != nil {
		t.Fatal(err)
	}

	if project.ID != projectpath.DeriveID(canonical) {
		t.Errorf("id = %q, want the derived id", project.ID)
	}
	if project.Path != canonical.Display {
		t.Errorf("path = %q, want %q", project.Path, canonical.Display)
	}
	if project.Profile != ProfileMinimal {
		t.Errorf("profile = %q, want the minimal default", project.Profile)
	}
	if project.Name != "alpha" {
		t.Errorf("name = %q, want alpha", project.Name)
	}
	if !project.ManifestPresent {
		t.Error("manifest presence was not recorded")
	}
	if project.RegisteredAt.IsZero() {
		t.Error("registration time was not set")
	}
	if registry.Len() != 1 {
		t.Errorf("len = %d, want 1", registry.Len())
	}
}

func TestAddRejectsUnknownProfile(t *testing.T) {
	isolate(t)
	canonical := makeDir(t, t.TempDir(), "alpha")
	if _, err := New().Add(canonical, Profile("god-mode"), false); !errors.Is(err, ErrInvalidProfile) {
		t.Errorf("got %v, want ErrInvalidProfile", err)
	}
}

func TestAddRejectsTheSameFolderTwice(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	canonical := makeDir(t, root, "alpha")

	registry := New()
	if _, err := registry.Add(canonical, ProfileFull, false); err != nil {
		t.Fatal(err)
	}

	_, err := registry.Add(canonical, ProfileFull, false)
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("got %v, want ErrAlreadyRegistered", err)
	}
	var duplicate *DuplicateError
	if !errors.As(err, &duplicate) {
		t.Fatalf("error is not a DuplicateError: %v", err)
	}
	if !duplicate.SameFile {
		t.Error("the duplicate should have been confirmed by the filesystem")
	}
	if registry.Len() != 1 {
		t.Errorf("len = %d, want 1", registry.Len())
	}
}

func TestAddRejectsAnAliasOfARegisteredFolder(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	target := makeDir(t, root, "real")

	link := filepath.Join(root, "link")
	if err := os.Symlink(target.Display, link); err != nil {
		t.Skipf("cannot create a symlink in this environment: %v", err)
	}
	viaLink, err := projectpath.Resolve(link)
	if err != nil {
		t.Fatal(err)
	}

	registry := New()
	if _, err := registry.Add(target, ProfileMinimal, false); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Add(viaLink, ProfileMinimal, false); !errors.Is(err, ErrAlreadyRegistered) {
		t.Errorf("a symlink alias was registered as a second project: %v", err)
	}
}

func TestAddKeepsSiblingFoldersSeparate(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	alpha := makeDir(t, root, "alpha")
	beta := makeDir(t, root, "beta")

	registry := New()
	first, err := registry.Add(alpha, ProfileMinimal, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Add(beta, ProfileFull, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Error("two folders share one project id")
	}
	if registry.Len() != 2 {
		t.Errorf("len = %d, want 2", registry.Len())
	}
}

func TestCaseDifferingSiblingsAreSeparateOnCaseSensitiveVolumes(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	upper := makeDir(t, root, "Sibling")
	if upper.CaseInsensitive {
		t.Skip("volume folds case, so a case-differing sibling cannot exist")
	}
	lower := makeDir(t, root, "sibling")

	registry := New()
	if _, err := registry.Add(upper, ProfileMinimal, false); err != nil {
		t.Fatal(err)
	}
	// These are genuinely different folders on this volume; folding must not
	// merge them.
	if _, err := registry.Add(lower, ProfileMinimal, false); err != nil {
		t.Errorf("case-differing siblings were merged: %v", err)
	}
}

func TestResolveByIDNamePathAndPrefix(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	canonical := makeDir(t, root, "alpha")

	registry := New()
	project, err := registry.Add(canonical, ProfileMinimal, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, reference := range []string{
		project.ID,
		"alpha",
		"ALPHA",
		canonical.Display,
		project.ID[:6],
	} {
		found, err := registry.Resolve(reference)
		if err != nil {
			t.Errorf("Resolve(%q) failed: %v", reference, err)
			continue
		}
		if found.ID != project.ID {
			t.Errorf("Resolve(%q) = %q, want %q", reference, found.ID, project.ID)
		}
	}

	if _, err := registry.Resolve("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
	if _, err := registry.Resolve(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty reference: got %v, want ErrNotFound", err)
	}
}

func TestResolveReportsAmbiguousNames(t *testing.T) {
	isolate(t)
	registry := New()
	// Two folders with the same base name under different parents.
	for _, parent := range []string{"one", "two"} {
		canonical := makeDir(t, t.TempDir(), parent, "shared")
		if _, err := registry.Add(canonical, ProfileMinimal, false); err != nil {
			t.Fatal(err)
		}
	}

	_, err := registry.Resolve("shared")
	if !errors.Is(err, ErrAmbiguousReference) {
		t.Fatalf("got %v, want ErrAmbiguousReference", err)
	}
	if !strings.Contains(err.Error(), "matches") {
		t.Errorf("error does not list the candidates: %v", err)
	}
}

func TestRemove(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	canonical := makeDir(t, root, "alpha")

	registry := New()
	project, err := registry.Add(canonical, ProfileMinimal, false)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := registry.Remove("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if removed.ID != project.ID {
		t.Errorf("removed %q, want %q", removed.ID, project.ID)
	}
	if registry.Len() != 0 {
		t.Errorf("len = %d, want 0", registry.Len())
	}

	// Removing a registration must not delete the folder.
	if _, err := os.Stat(canonical.Display); err != nil {
		t.Errorf("remove deleted project data: %v", err)
	}
	if _, err := registry.Remove("alpha"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	home := isolate(t)
	root := t.TempDir()

	registry := New()
	for _, name := range []string{"alpha", "beta"} {
		if _, err := registry.Add(makeDir(t, root, name), ProfileFull, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Save(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, FileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("registry file was not written: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Len() != 2 {
		t.Fatalf("len = %d, want 2", loaded.Len())
	}

	before, after := registry.List(), loaded.List()
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Path != after[i].Path {
			t.Errorf("record %d changed across the round trip:\n  %+v\n  %+v", i, before[i], after[i])
		}
		if !before[i].RegisteredAt.Equal(after[i].RegisteredAt) {
			t.Errorf("record %d timestamp changed: %v vs %v",
				i, before[i].RegisteredAt, after[i].RegisteredAt)
		}
	}

	// The stored document must carry the schema version.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, SchemaVersion)
	}
}

func TestLoadWithoutAFileReturnsAnEmptyRegistry(t *testing.T) {
	isolate(t)
	registry, err := Load()
	if err != nil {
		t.Fatalf("first run should not fail: %v", err)
	}
	if registry.Len() != 0 {
		t.Errorf("len = %d, want 0", registry.Len())
	}
}

func TestLoadRefusesCorruptRegistryRatherThanResetting(t *testing.T) {
	home := isolate(t)
	if err := os.WriteFile(filepath.Join(home, FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("a corrupt registry was silently accepted")
	}
}

func TestLoadRefusesANewerSchema(t *testing.T) {
	home := isolate(t)
	raw := []byte(`{"schema_version": 9999, "projects": []}`)
	if err := os.WriteFile(filepath.Join(home, FileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("got %v, want ErrSchemaTooNew", err)
	}
}

func TestLoadMigratesADocumentWithoutASchemaVersion(t *testing.T) {
	home := isolate(t)
	root := t.TempDir()
	canonical := makeDir(t, root, "legacy")

	// A document predating the schema-version field, and with no profile.
	raw := `{"projects":[{"id":"legacy-aaaaaaaa","name":"legacy","path":` +
		mustJSON(canonical.Display) + `,"key":` + mustJSON(canonical.Key) +
		`,"registered_at":"2026-07-01T00:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(home, FileName), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	projects := registry.List()
	if len(projects) != 1 {
		t.Fatalf("len = %d, want 1", len(projects))
	}
	if projects[0].Profile != ProfileMinimal {
		t.Errorf("profile = %q, want the minimal default after migration", projects[0].Profile)
	}

	// Saving must write the current version.
	if err := registry.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Len() != 1 {
		t.Errorf("len = %d, want 1 after re-save", reloaded.Len())
	}
}

func TestLoadRejectsDuplicateIdentifiers(t *testing.T) {
	home := isolate(t)
	raw := `{"schema_version":1,"projects":[
		{"id":"same-aaaaaaaa","name":"a","path":"/a","key":"/a","profile":"minimal"},
		{"id":"same-aaaaaaaa","name":"b","path":"/b","key":"/b","profile":"minimal"}]}`
	if err := os.WriteFile(filepath.Join(home, FileName), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "is used by both") {
		t.Errorf("got %v, want a duplicate identifier error", err)
	}
}

func TestSaveIsAtomicAndLeavesNoTemporaryFiles(t *testing.T) {
	home := isolate(t)
	registry := New()
	if _, err := registry.Add(makeDir(t, t.TempDir(), "alpha"), ProfileMinimal, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := registry.Save(); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", entry.Name())
		}
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only the registry file, found %v", names)
	}
}

func TestListIsSortedAndACopy(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	registry := New()
	for _, name := range []string{"zeta", "alpha", "middle"} {
		if _, err := registry.Add(makeDir(t, root, name), ProfileMinimal, false); err != nil {
			t.Fatal(err)
		}
	}

	list := registry.List()
	for i := 1; i < len(list); i++ {
		if list[i-1].ID > list[i].ID {
			t.Errorf("List is not sorted by id: %q before %q", list[i-1].ID, list[i].ID)
		}
	}

	// Mutating the returned slice must not affect the registry.
	list[0].Path = "/tampered"
	if registry.List()[0].Path == "/tampered" {
		t.Error("List returned a view into internal state")
	}
}

func mustJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
