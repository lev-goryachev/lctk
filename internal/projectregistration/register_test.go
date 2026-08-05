package projectregistration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/projectgrant"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

func TestRegisterPersistsTheAuthoritativePathAndScopedGrant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LCTK_HOME", home)
	projectDir := filepath.Join(t.TempDir(), "alpha")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := Register(projectDir, projectregistry.ProfileFull, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := projectregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	project, err := loaded.Resolve(result.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if project.Path != result.Project.Path || project.Profile != projectregistry.ProfileFull {
		t.Fatalf("project=%+v", project)
	}
	grants, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.ForProject(project.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
}
