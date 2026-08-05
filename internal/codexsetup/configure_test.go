package codexsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/codexconfig"
	"github.com/lev-goryachev/lctk/internal/projectgrant"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

func TestConfigureWritesTheScopedEntryAndExplicitUserEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LCTK_HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	grants := projectgrant.New()
	grant, err := grants.EnsureForProject("alpha-aaaaaaaa", projectgrant.DefaultClient, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := grants.Save(); err != nil {
		t.Fatal(err)
	}
	result, err := Configure(projectregistry.Project{ID: "alpha-aaaaaaaa"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Environment != projectgrant.EnvVarName("alpha-aaaaaaaa") || grant.Token == "" {
		t.Fatalf("result=%+v", result)
	}
	document, err := os.ReadFile(result.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(document), codexconfig.EntryName("alpha-aaaaaaaa")) || !strings.Contains(string(document), result.Environment) {
		t.Fatalf("config=%s", document)
	}
}
