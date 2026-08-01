package searchindex

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"testing"
)

func watchSet(t *testing.T, f *fixture) ([]string, bool) {
	t.Helper()
	directories, truncated, err := f.WatchSet(context.Background())
	if err != nil {
		t.Fatalf("WatchSet: %v", err)
	}
	sort.Strings(directories)
	return directories, truncated
}

func TestWatchSetSkipsWhatTheIndexerSkips(t *testing.T) {
	f := newFixture(t, smallLimits)
	seed(t, f)
	f.write(t, ".gitignore", "build/\n")
	f.write(t, "build/output.go", "package build\n")
	if err := os.MkdirAll(filepath.Join(f.workspace, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}

	directories, truncated := watchSet(t, f)
	if truncated {
		t.Fatal("a four-directory project reported a truncated watch set")
	}

	want := []string{".", "docs", "internal"}
	if len(directories) != len(want) {
		t.Fatalf("watch set = %v, want %v", directories, want)
	}
	for i, name := range want {
		if directories[i] != name {
			t.Fatalf("watch set = %v, want %v", directories, want)
		}
	}
}

// The watch set and the inventory must agree. A directory holding an indexed file
// but missing from the watch set is an edit the host can never see, and nothing
// would report it: search would simply keep answering from a stale generation.
func TestEveryIndexedFileLivesInAWatchedDirectory(t *testing.T) {
	f := newFixture(t, smallLimits)
	seed(t, f)
	f.write(t, "deep/nested/tree/file.go", "package tree\n")
	f.write(t, ".lctkignore", "!node_modules/\n")

	state := f.rebuild(t)
	directories, _ := watchSet(t, f)

	watched := make(map[string]bool, len(directories))
	for _, name := range directories {
		watched[name] = true
	}
	for name := range state.Files {
		parent := path.Dir(name)
		if !watched[parent] {
			t.Errorf("indexed file %q lives in %q, which the host is not told to watch", name, parent)
		}
	}
}

// A re-inclusion has to reach the watch set too, or the project could say "index
// this directory" and still never be told about a change in it.
func TestWatchSetHonoursReInclusion(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "node_modules/dep/index.js", "const x = 1;\n")

	directories, _ := watchSet(t, f)
	for _, name := range directories {
		if name == "node_modules" {
			t.Fatal("a default-excluded directory is in the watch set without being re-included")
		}
	}

	f.write(t, ".lctkignore", "!node_modules/\n")
	directories, _ = watchSet(t, f)
	found := false
	for _, name := range directories {
		if name == "node_modules/dep" {
			found = true
		}
	}
	if !found {
		t.Fatalf("re-included directory is missing from the watch set: %v", directories)
	}
}

func TestWatchSetReportsTruncationRatherThanAnIncompleteAnswer(t *testing.T) {
	f := newFixture(t, smallLimits)
	for i := 0; i < 8; i++ {
		f.write(t, filepath.ToSlash(filepath.Join("pkg", string(rune('a'+i)), "file.go")), "package p\n")
	}

	previous := MaxWatchDirectories
	MaxWatchDirectories = 4
	defer func() { MaxWatchDirectories = previous }()

	directories, truncated := watchSet(t, f)
	if !truncated {
		t.Fatalf("watch set of %d directories under a limit of 4 was not reported as truncated", len(directories))
	}
	if len(directories) > 4 {
		t.Fatalf("watch set exceeded its own limit: %d directories", len(directories))
	}
}
