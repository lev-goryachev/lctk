package searchindex

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// A save that changed nothing is not rare. A formatter with nothing to reformat,
// an editor writing on focus loss, and a build tool rewriting a generated file
// byte for byte all produce one. Publishing a generation for any of them would
// consume delta depth and bring the next full rebuild closer for no reason.
func TestASaveThatChangedNothingConsumesNoGeneration(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "app.go", "// Needle app\n")
	first := f.rebuild(t)

	// The same bytes, written again. This is what an editor does on focus loss.
	f.write(t, "app.go", "// Needle app\n")
	state, applied := f.report(t, Change{Path: "app.go"})

	if state.Generation != first.Generation {
		t.Errorf("generation moved from %d to %d for a write that changed nothing",
			first.Generation, state.Generation)
	}
	if state.DeltaDepth != first.DeltaDepth {
		t.Errorf("delta depth moved from %d to %d", first.DeltaDepth, state.DeltaDepth)
	}
	if !state.BuiltAt.Equal(first.BuiltAt) {
		t.Error("the build timestamp moved, so the index claims work it did not do")
	}
	if applied.Generations != 0 || applied.Changed != 0 || applied.Unchanged != 1 {
		t.Errorf("applied = %+v, want no generations, nothing changed, one unchanged", applied)
	}

	// The point of dropping the change is that the existing copy stays. A delta
	// that retracted the entry without re-adding it would report the same numbers
	// and lose the file.
	if got := paths(f.search(t, Request{Pattern: "Needle"})); len(got) != 1 || got[0] != "app.go" {
		t.Errorf("files after an unchanged write = %v, want app.go still searchable", got)
	}
}

// A batch is one entry per path, so an edit written and undone before the index
// caught up arrives as a single change whose content already matches. That is the
// shape of a proposal a developer rejected, and it should cost nothing.
//
// An edit reverted *after* the index caught up is two real changes and is meant
// to cost two: the index genuinely held the other content in between, and search
// results reflected it.
func TestAnEditUndoneBeforeTheIndexCaughtUpCostsNothing(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "app.go", "// Needle original\n")
	first := f.rebuild(t)

	f.write(t, "app.go", "// Needle proposed\n")
	f.write(t, "app.go", "// Needle original\n")

	state, applied := f.report(t, Change{Path: "app.go"})
	if state.Generation != first.Generation || applied.Generations != 0 {
		t.Errorf("generation = %d (was %d), applied = %+v: a reverted edit was indexed twice",
			state.Generation, first.Generation, applied)
	}
	if got := len(f.search(t, Request{Pattern: "Needle proposed"}).Matches); got != 0 {
		t.Errorf("the reverted content matches %d times, want 0", got)
	}
	if got := len(f.search(t, Request{Pattern: "Needle original"}).Matches); got != 1 {
		t.Errorf("the restored content matches %d times, want 1", got)
	}
}

// The batch is the unit a watcher submits, and it mixes real edits with saves that
// changed nothing. Applying only the real ones is what keeps a large batch from
// being judged bulk on a count it did not earn.
func TestOnlyTheWritesThatChangedAnythingAreApplied(t *testing.T) {
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

	// Eight paths submitted, one of them actually edited. Judged on the submitted
	// count this is 40% of the index and a full rebuild; judged on what changed it
	// is one file.
	changes := make([]Change, 0, 8)
	for i := 0; i < 8; i++ {
		changes = append(changes, Change{Path: "pkg/f" + strconv.Itoa(i) + ".go"})
	}
	f.write(t, "pkg/f3.go", "// Needle 3 edited\n")

	state, applied := f.report(t, changes...)
	if state.FullBuild {
		t.Error("a batch with one real edit in it rebuilt the whole index")
	}
	if applied.Changed != 1 || applied.Unchanged != 7 {
		t.Errorf("applied = %+v, want one changed and seven unchanged", applied)
	}
	if got := len(paths(f.search(t, Request{Pattern: "Needle"}))); got != 20 {
		t.Errorf("files = %d, want all 20 still searchable", got)
	}
	if got := paths(f.search(t, Request{Pattern: "Needle 3 edited"})); len(got) != 1 {
		t.Errorf("the one real edit is searchable in %v, want pkg/f3.go", got)
	}
}

// Delta depth is the budget that forces a full rebuild. A project being saved
// repeatedly with nothing changing must not spend it, or the rebuild arrives on a
// schedule set by an editor's autosave rather than by actual editing.
func TestNoOpBatchesDoNotSpendTheDeltaBudget(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "app.go", "// Needle app\n")
	first := f.rebuild(t)

	for i := 0; i < smallLimits.MaxDeltaGenerations+3; i++ {
		f.write(t, "app.go", "// Needle app\n")
		state := f.update(t, Change{Path: "app.go"})
		if state.Generation != first.Generation {
			t.Fatalf("save %d published generation %d, want %d", i, state.Generation, first.Generation)
		}
		if state.FullBuild != first.FullBuild {
			t.Fatalf("save %d escalated to a rebuild", i)
		}
	}
}

// A directory removed and restored with identical content in one batch is not a
// no-op, even though the file's bytes match what was indexed. The expansion of the
// removal has already retracted every path beneath it, so the restored file has to
// be added back or it disappears from the index while sitting on disk.
func TestAFileRestoredIdenticallyAfterItsDirectoryWasRemovedIsReAdded(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "pkg/a.go", "// Needle a\n")
	f.write(t, "keep.go", "// Needle keep\n")
	f.rebuild(t)

	if err := os.RemoveAll(filepath.Join(f.workspace, "pkg")); err != nil {
		t.Fatal(err)
	}
	f.write(t, "pkg/a.go", "// Needle a\n")

	f.update(t,
		Change{Path: "pkg", Deleted: true, Subtree: true},
		Change{Path: "pkg/a.go"},
	)

	if got := paths(f.search(t, Request{Pattern: "Needle a"})); len(got) != 1 || got[0] != "pkg/a.go" {
		t.Fatalf("the restored file is searchable in %v, want pkg/a.go", got)
	}
}

// A file that was never indexed and is ignored produces no tombstone. Retracting
// an absent entry would be harmless in the index and misleading in the report: it
// would make a batch that did nothing look like a batch that did something.
func TestAnIgnoredFileThatWasNeverIndexedIsNotReportedAsWork(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, ".gitignore", "secrets/\n")
	f.write(t, "app.go", "// Needle app\n")
	first := f.rebuild(t)

	f.write(t, "secrets/token.go", "// Needle secret\n")
	state, applied := f.report(t, Change{Path: "secrets/token.go"})

	if state.Generation != first.Generation || applied.Generations != 0 {
		t.Errorf("generation = %d (was %d), applied = %+v: an ignored new file published a generation",
			state.Generation, first.Generation, applied)
	}
	if got := len(f.search(t, Request{Pattern: "Needle secret"}).Matches); got != 0 {
		t.Errorf("the ignored file is searchable %d times, want 0", got)
	}
}
