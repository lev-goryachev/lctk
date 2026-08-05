package containerruntime

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExecutablePathIsInstallationOwned(t *testing.T) {
	t.Setenv(ExecutableOverride, "")
	home := t.TempDir()
	t.Setenv("LCTK_HOME", home)

	path, err := ExecutablePath()
	if err != nil {
		t.Fatal(err)
	}
	name := "podman"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	want := filepath.Join(home, "runtime", Provider, Version, "bin", name)
	if path != want {
		t.Fatalf("client path = %q, want %q", path, want)
	}
}

func TestExecutableOverrideIsAbsolute(t *testing.T) {
	t.Setenv(ExecutableOverride, filepath.Join("fixtures", "podman"))
	path, err := ExecutablePath()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, filepath.Join("fixtures", "podman")) {
		t.Fatalf("override path = %q", path)
	}
}

func TestCommandFailsClosedWhenClientIsMissing(t *testing.T) {
	t.Setenv(ExecutableOverride, filepath.Join(t.TempDir(), "absent-podman"))
	_, err := Command(t.Context(), "info")
	if err == nil || !strings.Contains(err.Error(), ErrClientMissing.Error()) {
		t.Fatalf("Command error = %v", err)
	}
}
