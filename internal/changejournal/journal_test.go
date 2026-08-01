package changejournal

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/fswatch"
)

func newJournal(t *testing.T, path string, options Options) *Journal {
	t.Helper()
	if options.Path == "" {
		options.Path = path
	}
	journal, err := Open("proj", options)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return journal
}

func written(path string) fswatch.Event {
	return fswatch.Event{Path: path, Kind: fswatch.Written, At: time.Unix(1700000000, 0).UTC()}
}

func TestOpeningAJournalAlwaysRecordsAGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proj.json")

	journal := newJournal(t, path, Options{})
	snapshot := journal.Snapshot()
	if snapshot.Gap == nil {
		t.Fatal("a freshly opened journal claims a complete record of a period it did not observe")
	}
	if snapshot.Gap.Reason != ReasonStarted {
		t.Fatalf("gap reason = %q, want %q", snapshot.Gap.Reason, ReasonStarted)
	}
}

func TestRepeatedSavesOfOnePathAreOneChange(t *testing.T) {
	journal := newJournal(t, filepath.Join(t.TempDir(), "proj.json"), Options{})

	for i := 0; i < 50; i++ {
		journal.Record(written("main.go"))
	}
	journal.Record(written("other.go"))

	pending := journal.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending = %+v, want one entry per path", pending)
	}
	// The surviving entry must carry the newest sequence, or a consumer that
	// committed through an earlier one would drop the later observation.
	if pending[0].Path != "main.go" || pending[0].Seq != 50 {
		t.Fatalf("pending[0] = %+v, want main.go at sequence 50", pending[0])
	}
}

func TestCommitDropsAppliedChangesAndKeepsTheGap(t *testing.T) {
	journal := newJournal(t, filepath.Join(t.TempDir(), "proj.json"), Options{})
	journal.Record(written("a.go"), written("b.go"))

	mark := journal.Mark()
	journal.Record(written("c.go"))
	journal.Commit(mark.Sequence, 7)

	snapshot := journal.Snapshot()
	if len(snapshot.Pending) != 1 || snapshot.Pending[0].Path != "c.go" {
		t.Fatalf("pending after commit = %+v, want only c.go", snapshot.Pending)
	}
	if snapshot.Checkpoint != mark.Sequence {
		t.Fatalf("checkpoint = %d, want %d", snapshot.Checkpoint, mark.Sequence)
	}
	if snapshot.Generation != 7 {
		t.Fatalf("generation = %d, want 7", snapshot.Generation)
	}
	if snapshot.Gap == nil {
		t.Fatal("applying an incomplete list was treated as making it complete")
	}
}

func TestReconciliationClearsTheGapItSetOutToClose(t *testing.T) {
	journal := newJournal(t, filepath.Join(t.TempDir(), "proj.json"), Options{})
	journal.Record(written("a.go"))

	mark := journal.Mark()
	journal.Reconciled(mark, 3)

	snapshot := journal.Snapshot()
	if snapshot.Gap != nil {
		t.Fatalf("gap survived a reconciliation that covered it: %+v", snapshot.Gap)
	}
	if len(snapshot.Pending) != 0 {
		t.Fatalf("pending = %+v, want empty after reconciliation", snapshot.Pending)
	}
}

// The dangerous case: a watcher loses events while the reconciliation is
// running. Clearing the gap then would declare the index current about changes
// nobody looked at.
func TestAGapOpenedDuringReconciliationIsNotCleared(t *testing.T) {
	journal := newJournal(t, filepath.Join(t.TempDir(), "proj.json"), Options{})

	mark := journal.Mark()
	journal.MarkGap(fswatch.ReasonOverflow, "buffer overflow", time.Now())
	journal.Reconciled(mark, 4)

	snapshot := journal.Snapshot()
	if snapshot.Gap == nil {
		t.Fatal("a gap that opened during reconciliation was cleared by it")
	}
	if snapshot.Gap.Reason != ReasonStarted {
		t.Fatalf("gap reason = %q, want the earliest reason %q", snapshot.Gap.Reason, ReasonStarted)
	}
}

func TestABulkChangeBecomesAGapRatherThanAHugePendingList(t *testing.T) {
	journal := newJournal(t, filepath.Join(t.TempDir(), "proj.json"), Options{MaxPending: 4})

	mark := journal.Mark()
	journal.Reconciled(mark, 1)
	if journal.Snapshot().Gap != nil {
		t.Fatal("the starting gap was not cleared, so the next assertion would prove nothing")
	}

	for _, name := range []string{"a", "b", "c", "d", "e"} {
		journal.Record(written(name + ".go"))
	}

	snapshot := journal.Snapshot()
	if snapshot.Gap == nil || snapshot.Gap.Reason != ReasonBulk {
		t.Fatalf("gap = %+v, want a %s gap", snapshot.Gap, ReasonBulk)
	}
	if len(snapshot.Pending) != 0 {
		t.Fatalf("pending = %+v, want the oversized list dropped rather than trimmed", snapshot.Pending)
	}
}

func TestTheRecordSurvivesAProcessRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proj.json")

	first := newJournal(t, path, Options{})
	mark := first.Mark()
	first.Reconciled(mark, 2)
	first.Record(written("pending.go"))
	first.Commit(0, 2)
	if err := first.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	second := newJournal(t, path, Options{})
	snapshot := second.Snapshot()
	if len(snapshot.Pending) != 1 || snapshot.Pending[0].Path != "pending.go" {
		t.Fatalf("pending after restart = %+v, want the unapplied change", snapshot.Pending)
	}
	if snapshot.Generation != 2 {
		t.Fatalf("generation after restart = %d, want 2", snapshot.Generation)
	}
	if snapshot.Gap == nil || snapshot.Gap.Reason != ReasonStarted {
		t.Fatalf("gap after restart = %+v, want the unobserved downtime recorded", snapshot.Gap)
	}
	if snapshot.Sequence < 1 {
		t.Fatalf("sequence after restart = %d, want the counter to continue", snapshot.Sequence)
	}
}

func TestAnUnreadableJournalIsResetRatherThanFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proj.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	journal := newJournal(t, path, Options{})
	snapshot := journal.Snapshot()
	if snapshot.Gap == nil || snapshot.Gap.Reason != ReasonUnreadable {
		t.Fatalf("gap = %+v, want the reset to be visible as %s", snapshot.Gap, ReasonUnreadable)
	}
	if len(snapshot.Pending) != 0 {
		t.Fatalf("pending = %+v, want empty after a reset", snapshot.Pending)
	}
}

func TestAJournalWrittenForAnotherProjectIsNotAdopted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proj.json")
	foreign := newJournal(t, path, Options{})
	foreign.Record(written("theirs.go"))
	if err := foreign.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	journal, err := Open("other", Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	snapshot := journal.Snapshot()
	if len(snapshot.Pending) != 0 {
		t.Fatalf("pending = %+v, want another project's record to be discarded", snapshot.Pending)
	}
	if snapshot.Gap == nil || snapshot.Gap.Reason != ReasonUnreadable {
		t.Fatalf("gap = %+v, want the discard to be visible", snapshot.Gap)
	}
}

func TestSaveIsOwnerOnlyAndAtomicallyReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "proj.json")
	journal := newJournal(t, path, Options{})
	journal.Record(written("a.go"))
	if err := journal.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// On Windows the mode bits are advisory and inherited ACLs are what actually
	// apply, so only the platforms that honour them are asserted on.
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("journal mode = %v, want owner-only", mode)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d files, want only the journal: %v", len(entries), entries)
	}
}
