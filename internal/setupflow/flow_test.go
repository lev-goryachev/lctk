package setupflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/lev-goryachev/lctk/internal/desktopinstall"
	"github.com/lev-goryachev/lctk/internal/installation"
	"github.com/lev-goryachev/lctk/internal/releasebundle"
	"github.com/lev-goryachev/lctk/internal/runtimeinstall"
	"github.com/lev-goryachev/lctk/internal/windowssetup"
)

type runtimeStub struct{ calls *[]string }

func (s runtimeStub) Inspect(releasebundle.Manifest) (runtimeinstall.Plan, error) {
	return runtimeinstall.Plan{Ready: true, DownloadBytes: 20}, nil
}
func (s runtimeStub) Install(context.Context, releasebundle.Manifest) error {
	*s.calls = append(*s.calls, "runtime")
	return nil
}

type coreStub struct {
	home  string
	calls *[]string
}

func (s coreStub) Inspect(releasebundle.Manifest) (installation.Plan, releasebundle.Artifact, error) {
	return installation.Plan{Ready: true, DownloadBytes: 10}, releasebundle.Artifact{}, nil
}
func (s coreStub) Install(context.Context, releasebundle.Manifest) (installation.Activation, error) {
	*s.calls = append(*s.calls, "core")
	path := filepath.Join(s.home, "versions", "1.0.0", "lctk-core.exe")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return installation.Activation{}, err
	}
	if err := os.WriteFile(path, []byte("core"), 0o700); err != nil {
		return installation.Activation{}, err
	}
	return installation.NewManager(s.home).Adopt(path, "1.0.0")
}

type desktopStub struct{ calls *[]string }

func (s desktopStub) Inspect(releasebundle.Manifest) (desktopinstall.Plan, error) {
	return desktopinstall.Plan{DownloadBytes: 5}, nil
}
func (s desktopStub) Install(context.Context, releasebundle.Manifest) (string, error) {
	*s.calls = append(*s.calls, "desktop")
	return "lctk.exe", nil
}

func TestInstallAppliesTheAcceptedDependencyOrder(t *testing.T) {
	home := t.TempDir()
	var calls []string
	manager := &Manager{
		Home: home, ManifestSource: "https://example/manifest",
		Runtime: runtimeStub{&calls}, Core: coreStub{home, &calls}, Desktop: desktopStub{&calls},
		ProbeHost: func(context.Context) (windowssetup.Status, error) {
			return windowssetup.Status{Supported: true, WSLReady: true}, nil
		},
		Run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
			calls = append(calls, "bootstrap")
			// ActiveExecutable returns the native release filename because setup is
			// exercised on every supported CI host, even though the desktop setup
			// itself is released only for Windows.
			coreName := "lctk-core"
			if runtime.GOOS == "windows" {
				coreName += ".exe"
			}
			if filepath.Base(executable) != coreName || !contains(args, "--yes") {
				t.Fatalf("bootstrap=%s %v", executable, args)
			}
			return []byte(`{"ready":true}`), nil
		},
	}
	if err := manager.Install(t.Context(), releasebundle.Manifest{Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"runtime", "core", "bootstrap", "desktop"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestInstallStopsForARequiredReboot(t *testing.T) {
	var enabled, resumed bool
	manager := &Manager{
		Runtime: runtimeStub{new([]string)}, Core: coreStub{t.TempDir(), new([]string)}, Desktop: desktopStub{new([]string)},
		ProbeHost: func(context.Context) (windowssetup.Status, error) {
			return windowssetup.Status{Supported: true, RequiresEnablement: true}, nil
		},
		EnableWSL:      func(context.Context) (bool, error) { enabled = true; return true, nil },
		RegisterResume: func() error { resumed = true; return nil },
	}
	if err := manager.Install(t.Context(), releasebundle.Manifest{}); !errors.Is(err, ErrRebootRequired) {
		t.Fatalf("err=%v", err)
	}
	if !enabled || !resumed {
		t.Fatalf("enabled=%t resumed=%t", enabled, resumed)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
