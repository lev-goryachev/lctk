package hostsettings

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSettings(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAMissingDocumentYieldsTheShippedDefaults(t *testing.T) {
	settings, err := LoadFrom(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if settings != Defaults {
		t.Fatalf("settings = %+v, want the shipped defaults %+v", settings, Defaults)
	}
	if got := settings.Watch.Debounce(); got != 3*time.Second {
		t.Fatalf("default debounce = %s, want 3s", got)
	}
}

func TestOnlyTheStatedFieldsOverrideTheDefaults(t *testing.T) {
	path := writeSettings(t, `{"schema_version":1,"watch":{"debounce_ms":5000}}`)

	settings, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := settings.Watch.Debounce(); got != 5*time.Second {
		t.Fatalf("debounce = %s, want 5s", got)
	}
	if settings.Watch.MaxWatchedDirectories != Defaults.Watch.MaxWatchedDirectories {
		t.Fatalf("an unstated field changed: %+v", settings.Watch)
	}
}

// A settings file is something a person wrote deliberately. Ignoring a broken one
// would leave them believing a setting applies when it does not.
func TestAMalformedDocumentIsAnErrorRatherThanASilentFallback(t *testing.T) {
	if _, err := LoadFrom(writeSettings(t, "{not json")); err == nil {
		t.Fatal("a malformed settings document was accepted")
	}
	if _, err := LoadFrom(writeSettings(t, `{"schema_version":99}`)); err == nil {
		t.Fatal("a settings document from a newer build was accepted")
	}
}

func TestDebounceIsClampedIntoTheSupportedRange(t *testing.T) {
	cases := []struct {
		milliseconds int
		want         time.Duration
	}{
		{milliseconds: 1, want: MinDebounce},
		{milliseconds: 3000, want: 3 * time.Second},
		{milliseconds: 600000, want: MaxDebounce},
	}
	for _, c := range cases {
		got := Watch{DebounceMS: c.milliseconds}.Debounce()
		if got != c.want {
			t.Errorf("Watch{DebounceMS: %d}.Debounce() = %s, want %s", c.milliseconds, got, c.want)
		}
	}
}

// A ceiling below the window would settle every change at once and make the
// window meaningless, so the ceiling is never allowed below it.
func TestTheSettleCeilingIsNeverBelowTheDebounceWindow(t *testing.T) {
	watch := Watch{DebounceMS: 10000, MaxDebounceMS: 2000}
	if got := watch.SettleCeiling(); got < watch.Debounce() {
		t.Fatalf("SettleCeiling() = %s, below the debounce window %s", got, watch.Debounce())
	}
}

// A project may propose a window; it may not escape the host's bounds.
func TestAProjectProposalIsClampedNotObeyed(t *testing.T) {
	base := Defaults.Watch

	if got := base.WithProjectDebounce(8000).Debounce(); got != 8*time.Second {
		t.Fatalf("an in-range project proposal was not applied: %s", got)
	}
	if got := base.WithProjectDebounce(3600000).Debounce(); got != MaxDebounce {
		t.Fatalf("a project escaped the ceiling: %s", got)
	}
	if got := base.WithProjectDebounce(1).Debounce(); got != MinDebounce {
		t.Fatalf("a project escaped the floor: %s", got)
	}
	if got := base.WithProjectDebounce(0).Debounce(); got != base.Debounce() {
		t.Fatalf("an absent proposal changed the window: %s", got)
	}
}
