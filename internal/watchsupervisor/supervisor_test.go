package watchsupervisor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/changejournal"
	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/hostsettings"
	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/projectmanifest"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

// The debounce floor is 200ms, so a test that waits for a settle waits about
// that long. Budgets here are generous multiples of it; nothing asserts on speed.
const (
	testDebounce = 200 * time.Millisecond
	testBudget   = 10 * time.Second
)

// clock is a hand-advanced time source, so the idle policy can be exercised
// without a test that sleeps for its duration.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type fakeService struct {
	server *httptest.Server

	mu        sync.Mutex
	truncated bool
}

func newFakeService(t *testing.T, directories []string) *fakeService {
	t.Helper()
	service := &fakeService{}
	service.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/watchset" {
			http.NotFound(w, r)
			return
		}
		service.mu.Lock()
		truncated := service.truncated
		service.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"directories": directories,
			"count":       len(directories),
			"truncated":   truncated,
			"limit":       100,
		})
	}))
	t.Cleanup(service.server.Close)
	return service
}

func (f *fakeService) address() string {
	return strings.TrimPrefix(f.server.URL, "http://")
}

// registryWith builds a registry holding one project without touching real user
// state. It goes through the store rather than constructing the type directly, so
// the test exercises the same document the daemon reads.
func registryWith(t *testing.T, project projectregistry.Project) *projectregistry.Registry {
	t.Helper()
	home := t.TempDir()
	t.Setenv(lctkhome.EnvOverride, home)

	document := struct {
		SchemaVersion int                       `json:"schema_version"`
		Projects      []projectregistry.Project `json:"projects"`
	}{SchemaVersion: projectregistry.SchemaVersion, Projects: []projectregistry.Project{project}}

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, projectregistry.FileName), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := projectregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

type harness struct {
	*Supervisor
	root     string
	project  projectregistry.Project
	status   projectstack.Status
	journals string
	clock    *clock
}

func newHarness(t *testing.T, service *fakeService, running *bool) *harness {
	t.Helper()

	root := t.TempDir()
	journals := t.TempDir()
	project := projectregistry.Project{ID: "proj", Name: "proj", Path: root, Key: root, Profile: projectregistry.ProfileMinimal}
	status := projectstack.Status{ProjectID: project.ID, State: projectstack.StateRunning, ServiceAddress: service.address()}
	tick := &clock{at: time.Unix(1700000000, 0).UTC()}

	registry := registryWith(t, project)

	supervisor := New(Options{
		Registry: func() (*projectregistry.Registry, error) { return registry, nil },
		Status: func(context.Context, projectregistry.Project) (projectstack.Status, error) {
			if running != nil && !*running {
				return projectstack.Status{ProjectID: project.ID, State: projectstack.StateStopped}, nil
			}
			return status, nil
		},
		Settings: func() (hostsettings.Settings, error) {
			return hostsettings.Settings{
				SchemaVersion: hostsettings.SchemaVersion,
				Watch: hostsettings.Watch{
					DebounceMS:            int(testDebounce.Milliseconds()),
					MaxDebounceMS:         1000,
					MaxWatchedDirectories: 100,
					IdleStopSeconds:       60,
				},
			}, nil
		},
		Manifest:  func(string) (projectmanifest.Result, error) { return projectmanifest.Result{}, nil },
		NewClient: codeintel.New,
		Journal: func(projectID string) (*changejournal.Journal, error) {
			return changejournal.Open(projectID, changejournal.Options{
				Path: filepath.Join(journals, projectID+".json"),
				Now:  tick.Now,
			})
		},
		SweepInterval: time.Hour,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:           tick.Now,
	})
	t.Cleanup(supervisor.Close)

	return &harness{
		Supervisor: supervisor,
		root:       root,
		project:    project,
		status:     status,
		journals:   journals,
		clock:      tick,
	}
}

func (h *harness) write(t *testing.T, relative, content string) {
	t.Helper()
	path := filepath.Join(h.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// awaitView polls the supervisor's own view rather than a fixed sleep, so the
// test states what it is waiting for instead of guessing how long it takes.
func (h *harness) awaitView(t *testing.T, what string, match func(View) bool) View {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	var last View
	for time.Now().Before(deadline) {
		if view, ok := h.View(h.project.ID); ok {
			last = view
			if match(view) {
				return view
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no view matched %s within %s; last was %+v", what, testBudget, last)
	return View{}
}

func (h *harness) storedJournal(t *testing.T) changejournal.Snapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.journals, h.project.ID+".json"))
	if err != nil {
		t.Fatalf("read stored journal: %v", err)
	}
	var snapshot changejournal.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode stored journal: %v", err)
	}
	return snapshot
}

// awaitStored waits for the document on disk, which is what a restart or a status
// command would read. The in-memory view can be ahead of it by one settle.
func (h *harness) awaitStored(t *testing.T, what string, match func(changejournal.Snapshot) bool) changejournal.Snapshot {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	var last changejournal.Snapshot
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(h.journals, h.project.ID+".json"))
		if err == nil {
			var snapshot changejournal.Snapshot
			if json.Unmarshal(raw, &snapshot) == nil {
				last = snapshot
				if match(snapshot) {
					return snapshot
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the stored journal never matched %s within %s; last was %+v", what, testBudget, last)
	return changejournal.Snapshot{}
}

func TestASweepStartsWatchingARunningProject(t *testing.T) {
	// "gone" is in the service's answer but not on disk, which is the ordinary
	// race between the service enumerating and the host registering. It is
	// skipped rather than treated as a failure.
	service := newFakeService(t, []string{".", "internal", "gone"})
	h := newHarness(t, service, nil)
	if err := os.MkdirAll(filepath.Join(h.root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}

	h.Sweep(context.Background())

	view, ok := h.View(h.project.ID)
	if !ok {
		t.Fatal("a running project was not picked up by the sweep")
	}
	if !view.Watching || view.Directories != 2 {
		t.Fatalf("view = %+v, want the two directories that exist to be watched", view)
	}
	if view.DebounceSeconds != testDebounce.Seconds() {
		t.Fatalf("debounce = %v seconds, want %v", view.DebounceSeconds, testDebounce.Seconds())
	}
}

// A watched project must have a document on disk before anything happens to it.
// Without one, a status command reports "never observed" about a project that is
// being observed right now, which is the opposite of the truth. This was found by
// running the daemon against a real repository, not by a fixture.
func TestAWatchedProjectHasAJournalBeforeAnythingChanges(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)

	h.Sweep(context.Background())

	stored := h.awaitStored(t, "a journal written at startup", func(s changejournal.Snapshot) bool {
		return s.Gap != nil
	})
	if len(stored.Pending) != 0 {
		t.Fatalf("stored pending = %+v, want an empty record", stored.Pending)
	}
}

func TestASavedFileIsJournaledAndSettles(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)
	h.Sweep(context.Background())

	h.write(t, "main.go", "package main\n")

	view := h.awaitView(t, "the saved file becoming a pending change", func(v View) bool {
		return v.Pending == 1 && !v.SettledAt.IsZero()
	})
	if view.Sequence == 0 {
		t.Fatal("the observation was not sequenced")
	}

	h.awaitStored(t, "the saved file persisted at settle", func(s changejournal.Snapshot) bool {
		return len(s.Pending) == 1 && s.Pending[0].Path == "main.go"
	})
}

// The debounce window exists so an editor's save-then-format-then-save burst is
// one update, not three.
func TestABurstOfSavesCollapsesIntoOnePendingChange(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)
	h.Sweep(context.Background())

	for i := 0; i < 20; i++ {
		h.write(t, "main.go", "package main // "+time.Duration(i).String()+"\n")
	}

	view := h.awaitView(t, "the burst settling", func(v View) bool {
		return v.Pending == 1 && !v.SettledAt.IsZero()
	})
	if view.Pending != 1 {
		t.Fatalf("pending = %d, want one change per path", view.Pending)
	}
}

func TestAStoppedProjectReleasesItsWatcherAndRecordsAGap(t *testing.T) {
	service := newFakeService(t, []string{"."})
	running := true
	h := newHarness(t, service, &running)

	h.Sweep(context.Background())
	if _, ok := h.View(h.project.ID); !ok {
		t.Fatal("the project was not being watched to begin with")
	}

	running = false
	h.Sweep(context.Background())

	if _, ok := h.View(h.project.ID); ok {
		t.Fatal("a stopped project kept its watcher")
	}
	stored := h.storedJournal(t)
	if stored.Gap == nil || stored.Gap.Reason == "" {
		t.Fatalf("stored gap = %+v, want the unobserved period recorded", stored.Gap)
	}
}

// A request to a project is what "on-demand wakeup" means: the client using the
// project is the reason to spend handles on watching it.
func TestARequestWakesAProjectTheSweepHasNotReached(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)

	h.Wake(h.project, h.status)

	h.awaitView(t, "the woken project", func(v View) bool { return v.Watching })
}

func TestWakingATornDownSupervisorDoesNothing(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)

	h.Close()
	h.Wake(h.project, h.status)

	if _, ok := h.View(h.project.ID); ok {
		t.Fatal("a closed supervisor started a watcher")
	}
}

// A project with more directories than the service will describe is watched only
// in part, and the record has to say so rather than look complete.
func TestATruncatedWatchSetIsRecordedAsAGap(t *testing.T) {
	service := newFakeService(t, []string{"."})
	service.mu.Lock()
	service.truncated = true
	service.mu.Unlock()

	h := newHarness(t, service, nil)
	h.Sweep(context.Background())

	view, ok := h.View(h.project.ID)
	if !ok {
		t.Fatal("the project was not watched at all")
	}
	if view.Gap == nil {
		t.Fatal("a partial watch set was reported as a complete record")
	}
	if view.Gap.Reason != changejournal.ReasonStarted && view.Gap.Reason != ReasonWatchSetIncomplete {
		t.Fatalf("gap reason = %q, want the incompleteness recorded", view.Gap.Reason)
	}
}

func TestAnIdleProjectReleasesItsWatcher(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)
	h.Sweep(context.Background())

	if _, ok := h.View(h.project.ID); !ok {
		t.Fatal("the project was not being watched to begin with")
	}

	h.clock.Advance(2 * time.Hour)
	h.Sweep(context.Background())

	if _, ok := h.View(h.project.ID); ok {
		t.Fatal("a project nobody has used kept its watcher")
	}
}

func TestAProjectWhoseServiceIsUnreachableIsNotWatched(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)
	service.server.Close()

	h.Sweep(context.Background())

	if _, ok := h.View(h.project.ID); ok {
		t.Fatal("a watcher was started without knowing what to watch")
	}
}
