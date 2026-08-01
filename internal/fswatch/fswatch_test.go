package fswatch

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// settle is how long a test waits for a native event. Filesystem notification is
// asynchronous on every platform, so the budget is generous; the tests assert on
// what arrives, never on how quickly.
const settle = 10 * time.Second

type harness struct {
	*Watcher
	root string
}

func newHarness(t *testing.T, directories ...string) *harness {
	t.Helper()
	root := t.TempDir()
	if len(directories) == 0 {
		directories = []string{"."}
	}
	watcher, err := Start(Options{Root: root, Directories: directories})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = watcher.Close() })
	return &harness{Watcher: watcher, root: root}
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

// await drains events until one satisfies the predicate, and reports everything
// it saw when it does not. Asserting on a specific event rather than on the next
// event keeps the test independent of how a platform splits a save into
// notifications.
func (h *harness) await(t *testing.T, what string, match func(Event) bool) Event {
	t.Helper()
	deadline := time.After(settle)
	var seen []Event
	for {
		select {
		case event := <-h.Events():
			seen = append(seen, event)
			if match(event) {
				return event
			}
		case <-deadline:
			t.Fatalf("no event matched %s within %s; saw %+v", what, settle, seen)
			return Event{}
		}
	}
}

func (h *harness) awaitGap(t *testing.T, what string) Gap {
	t.Helper()
	select {
	case gap := <-h.Gaps():
		return gap
	case <-time.After(settle):
		t.Fatalf("no gap was recorded for %s within %s", what, settle)
		return Gap{}
	}
}

func TestASavedFileBecomesAProjectRelativeEvent(t *testing.T) {
	h := newHarness(t)
	h.write(t, "main.go", "package main\n")

	event := h.await(t, "a write to main.go", func(e Event) bool {
		return e.Path == "main.go" && e.Kind == Written
	})
	if event.Directory {
		t.Error("a regular file was reported as a directory")
	}
	if event.At.IsZero() {
		t.Error("the event carries no timestamp")
	}
}

// A directory created with content already in it is the case a naive watcher
// loses: the files land before a watch can be placed on their new parent.
func TestANewDirectoryIsAdoptedAlongWithWhatIsAlreadyInside(t *testing.T) {
	h := newHarness(t)

	staging := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(filepath.Join(staging, "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.go", filepath.Join("deep", "b.go")} {
		if err := os.WriteFile(filepath.Join(staging, name), []byte("package pkg\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Moved into place in one operation, so the whole tree appears at once and
	// the watcher has no chance to observe the individual writes.
	if err := os.Rename(staging, filepath.Join(h.root, "pkg")); err != nil {
		t.Fatal(err)
	}

	h.await(t, "the adopted nested file", func(e Event) bool {
		return e.Path == "pkg/deep/b.go" && e.Kind == Written && !e.Directory
	})
	if !h.isWatched("pkg/deep") {
		t.Error("the new nested directory was not registered, so later edits in it would be invisible")
	}
}

// A removed directory takes its files with it, and once it is gone the
// filesystem can no longer say it was a directory. The event has to carry that.
func TestARemovedDirectoryIsReportedAsADirectory(t *testing.T) {
	h := newHarness(t)
	h.write(t, "pkg/a.go", "package pkg\n")
	h.await(t, "the directory being created", func(e Event) bool {
		return e.Path == "pkg" && e.Directory
	})
	// Reporting the directory must imply having registered it. If it did not, a
	// directory removed straight after being created would be reported as a file.
	if !h.isWatched("pkg") {
		t.Fatal("a new directory was reported before it was registered")
	}

	if err := os.RemoveAll(filepath.Join(h.root, "pkg")); err != nil {
		t.Fatal(err)
	}
	event := h.await(t, "the removal of pkg", func(e Event) bool {
		return e.Path == "pkg" && e.Kind == Removed
	})
	if !event.Directory {
		t.Fatal("a removed directory was reported as a file, so its contents would stay in the index")
	}
	if h.isWatched("pkg") {
		t.Error("the watch on a removed directory was not released")
	}
}

// Windows reports a write against the containing directory too. A directory has
// no content, so passing that on would double every save.
func TestADirectoryIsNeverReportedAsWritten(t *testing.T) {
	h := newHarness(t)
	h.write(t, "pkg/a.go", "package pkg\n")
	h.await(t, "the new directory", func(e Event) bool { return e.Path == "pkg" && e.Directory })

	h.write(t, "pkg/a.go", "package pkg // edited\n")
	h.await(t, "the edit", func(e Event) bool { return e.Path == "pkg/a.go" && e.Kind == Written })

	for {
		select {
		case event := <-h.Events():
			if event.Path == "pkg" && event.Kind == Written {
				t.Fatal("a directory was reported as written")
			}
		case <-time.After(500 * time.Millisecond):
			return
		}
	}
}

// One removal can be reported twice: the parent's watch sees the child go, and
// the directory's own watch sees itself go. The second report must not describe
// the directory as a file — a consumer keeping one entry per path would let it
// overwrite the first, and every file that was inside would stay in the index.
func TestARepeatedRemovalStillReportsADirectory(t *testing.T) {
	h := newHarness(t)
	h.write(t, "pkg/a.go", "package pkg\n")
	h.await(t, "the new directory", func(e Event) bool { return e.Path == "pkg" && e.Directory })

	if err := os.RemoveAll(filepath.Join(h.root, "pkg")); err != nil {
		t.Fatal(err)
	}
	h.await(t, "the removal", func(e Event) bool { return e.Path == "pkg" && e.Kind == Removed })

	// Replay the same native event, which is what a second watch reporting the
	// same removal amounts to.
	h.translate(fsnotify.Event{Name: filepath.Join(h.root, "pkg"), Op: fsnotify.Remove})
	repeat := h.await(t, "the repeated removal", func(e Event) bool {
		return e.Path == "pkg" && e.Kind == Removed
	})
	if !repeat.Directory {
		t.Fatal("a repeated directory removal was downgraded to a file removal")
	}
}

func TestVersionControlMetadataIsNeverObserved(t *testing.T) {
	h := newHarness(t)
	h.write(t, ".git/objects/ab/cdef", "binary\n")
	h.write(t, "marker.go", "package main\n")

	// The marker is written second, so seeing it means anything .git would have
	// produced has already had its chance to arrive.
	h.await(t, "the marker file", func(e Event) bool { return e.Path == "marker.go" })

	for {
		select {
		case event := <-h.Events():
			if filepath.ToSlash(event.Path) == ".git" ||
				len(event.Path) > 5 && event.Path[:5] == ".git/" {
				t.Fatalf("version-control metadata produced an event: %+v", event)
			}
		default:
			return
		}
	}
}

func TestAProjectLargerThanTheWatchBudgetRecordsACapacityGap(t *testing.T) {
	root := t.TempDir()
	var directories []string
	for i := 0; i < 6; i++ {
		name := "pkg" + strconv.Itoa(i)
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		directories = append(directories, name)
	}

	watcher, err := Start(Options{Root: root, Directories: append([]string{"."}, directories...), MaxDirectories: 3})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = watcher.Close() })

	h := &harness{Watcher: watcher, root: root}
	gap := h.awaitGap(t, "a project over the watch budget")
	if gap.Reason != ReasonCapacity {
		t.Fatalf("gap reason = %q, want %q", gap.Reason, ReasonCapacity)
	}
	if watcher.Watched() > 3 {
		t.Fatalf("registered %d directories against a budget of 3", watcher.Watched())
	}

	// Every directory past the budget would report the same thing. One notice is
	// the whole message; repeating it per directory would bury a real second
	// reason under a hundred copies of the first.
	select {
	case extra := <-h.Gaps():
		t.Fatalf("the over-budget notice was repeated: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	h := newHarness(t)
	if err := h.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestPathsOutsideTheRootAreRefused(t *testing.T) {
	h := newHarness(t)
	if _, ok := h.relative(filepath.Join(filepath.Dir(h.root), "elsewhere", "file.go")); ok {
		t.Fatal("a path beside the project was accepted as project-relative")
	}
	if _, ok := h.relative(h.root); ok {
		t.Fatal("the root itself was accepted as a changed path")
	}
}

func TestExcludedPath(t *testing.T) {
	cases := map[string]bool{
		".":                   false,
		"main.go":             false,
		".gitignore":          false,
		".git":                true,
		".git/config":         true,
		"vendor/.git/objects": true,
		".hg/store":           true,
		".svn":                true,
	}
	for input, want := range cases {
		if got := excludedPath(input); got != want {
			t.Errorf("excludedPath(%q) = %v, want %v", input, got, want)
		}
	}
}
