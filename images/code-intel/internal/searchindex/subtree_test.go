package searchindex

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// A removed directory takes its files with it. Once it is gone nothing can
// enumerate them, so the batch says "subtree" and the index has to expand it.
// Without this, every file that was inside stays searchable forever.
func TestARemovedDirectoryTakesItsFilesOutOfTheIndex(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "keep.go", "// Needle keep\n")
	f.write(t, "pkg/a.go", "// Needle a\n")
	f.write(t, "pkg/deep/b.go", "// Needle b\n")
	f.rebuild(t)

	if got := len(paths(f.search(t, Request{Pattern: "Needle"}))); got != 3 {
		t.Fatalf("before the removal, matching files = %d, want 3", got)
	}

	if err := os.RemoveAll(filepath.Join(f.workspace, "pkg")); err != nil {
		t.Fatal(err)
	}
	state := f.update(t, Change{Path: "pkg", Deleted: true, Subtree: true})

	if state.FileCount != 1 {
		t.Fatalf("file count = %d, want only keep.go", state.FileCount)
	}
	after := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(after) != 1 || after[0] != "keep.go" {
		t.Fatalf("after the removal, files = %v, want only keep.go", after)
	}
}

// A directory deletion without the flag must not be read as a subtree deletion.
// It names one path, and one path is what it retracts.
func TestADeletionWithoutTheSubtreeFlagRetractsOnlyItsOwnPath(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "pkg/a.go", "// Needle a\n")
	f.rebuild(t)

	f.update(t, Change{Path: "pkg", Deleted: true})
	if got := paths(f.search(t, Request{Pattern: "Needle"})); len(got) != 1 {
		t.Fatalf("files = %v, want pkg/a.go untouched", got)
	}
}

// A directory removed and immediately replaced arrives as one batch. The
// replacement must survive the expansion of the removal.
func TestAFileRewrittenAfterItsDirectoryWasRemovedSurvivesTheSameBatch(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "pkg/a.go", "// Needle old\n")
	f.write(t, "pkg/gone.go", "// Needle gone\n")
	f.rebuild(t)

	if err := os.RemoveAll(filepath.Join(f.workspace, "pkg")); err != nil {
		t.Fatal(err)
	}
	f.write(t, "pkg/a.go", "// Needle new\n")

	f.update(t,
		Change{Path: "pkg", Deleted: true, Subtree: true},
		Change{Path: "pkg/a.go"},
	)

	if got := paths(f.search(t, Request{Pattern: "Needle new"})); len(got) != 1 || got[0] != "pkg/a.go" {
		t.Fatalf("the rewritten file did not survive its directory's removal: %v", got)
	}
	if got := f.search(t, Request{Pattern: "Needle gone"}); len(got.Matches) != 0 {
		t.Fatalf("a file removed with its directory still matches: %+v", got.Matches)
	}
	if got := f.search(t, Request{Pattern: "Needle old"}); len(got.Matches) != 0 {
		t.Fatalf("the previous content of the rewritten file still matches: %+v", got.Matches)
	}
}

// A batch touching much of the index is a checkout or a generator, not editing.
// Applying it as a delta taxes every later query with tombstones to resolve.
func TestABulkChangeIsRebuiltRatherThanApplied(t *testing.T) {
	limits := Limits{
		MaxFileBytes:        4096,
		MaxDeltaGenerations: 50,
		KeepGenerations:     3,
		BulkChangeFloor:     4,
		BulkChangePercent:   25,
	}
	f := newFixture(t, limits)
	for i := 0; i < 20; i++ {
		f.write(t, "pkg/f"+strconv.Itoa(i)+".go", "// Needle "+strconv.Itoa(i)+"\n")
	}
	f.rebuild(t)

	// The files are rewritten, not merely named. Escalation is judged on what
	// actually changed, so a batch of paths whose content still matches the index
	// is not a bulk change at all — it is nothing.
	edit := func(t *testing.T, count int, revision string) []Change {
		t.Helper()
		changes := make([]Change, 0, count)
		for i := 0; i < count; i++ {
			name := "pkg/f" + strconv.Itoa(i) + ".go"
			f.write(t, name, "// Needle "+strconv.Itoa(i)+" "+revision+"\n")
			changes = append(changes, Change{Path: name})
		}
		return changes
	}

	// Three of twenty is under the floor and under the share: a delta.
	state := f.update(t, edit(t, 3, "second")...)
	if state.FullBuild {
		t.Fatal("a three-file change rebuilt the whole index")
	}

	// Eight of twenty clears the floor and is well past a quarter: a rebuild.
	state = f.update(t, edit(t, 8, "third")...)
	if !state.FullBuild {
		t.Fatal("a change touching 40% of the index was applied as a delta")
	}
	if state.DeltaDepth != 0 {
		t.Fatalf("delta depth = %d after a rebuild, want 0", state.DeltaDepth)
	}
	if got := len(paths(f.search(t, Request{Pattern: "Needle"}))); got != 20 {
		t.Fatalf("after the rebuild, matching files = %d, want 20", got)
	}
}

// The floor exists so a small project is not rebuilt on every second edit, where
// a quarter of the index is two files.
func TestTheBulkFloorProtectsASmallProject(t *testing.T) {
	policy := Limits{BulkChangeFloor: 500, BulkChangePercent: 25}.withDefaults()
	cases := []struct {
		count, indexed int
		want           bool
	}{
		{count: 2, indexed: 4, want: false},
		{count: 499, indexed: 500, want: false},
		{count: 500, indexed: 100000, want: false},
		{count: 500, indexed: 2000, want: true},
		{count: 600, indexed: 0, want: true},
	}
	for _, c := range cases {
		if got := policy.bulk(c.count, c.indexed); got != c.want {
			t.Errorf("bulk(%d changes, %d indexed) = %v, want %v", c.count, c.indexed, got, c.want)
		}
	}
}
