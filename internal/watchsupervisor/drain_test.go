package watchsupervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/changejournal"
	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/fswatch"
)

// awaitApply waits for the index to receive a batch matching the predicate.
func (h *harness) awaitApply(t *testing.T, service *fakeService, what string, match func([][]codeintel.Change) bool) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) {
		if applied := service.appliedPaths(); match(applied) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the index never received %s within %s; it received %+v",
		what, testBudget, service.appliedPaths())
}

// The record starts incomplete, because the daemon was not watching before it
// started. An incomplete record cannot be applied: the pending list is a lower
// bound, so the only correct first move is to reconcile.
func TestAnIncompleteRecordIsReconciledRatherThanApplied(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)

	h.Sweep(context.Background())

	h.awaitView(t, "the starting gap being closed", func(v View) bool {
		return v.Gap == nil && v.Generation > 0
	})
	if service.reconcileCount() == 0 {
		t.Fatal("the index was never reconciled after a period of no observation")
	}
	if applied := service.appliedPaths(); len(applied) != 0 {
		t.Fatalf("an incomplete record was applied as a batch: %+v", applied)
	}
}

func TestASavedFileReachesTheIndexWithoutBeingAskedTo(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)
	h.Sweep(context.Background())
	h.awaitView(t, "the starting gap being closed", func(v View) bool { return v.Gap == nil })

	h.write(t, "main.go", "package main\n")

	h.awaitApply(t, service, "the saved file", func(batches [][]codeintel.Change) bool {
		for _, batch := range batches {
			for _, change := range batch {
				if change.Path == "main.go" && !change.Deleted {
					return true
				}
			}
		}
		return false
	})

	view := h.awaitView(t, "the checkpoint catching up", func(v View) bool {
		return v.Pending == 0 && v.Checkpoint == v.Sequence
	})
	if view.Generation == 0 {
		t.Fatal("the checkpoint records no index generation, so a restart could not tell what it describes")
	}
	if view.LastError != "" {
		t.Fatalf("last error = %q, want none", view.LastError)
	}
}

// A removed directory has to reach the index as a subtree deletion. Sent as an
// ordinary path it would retract one entry the index does not hold and leave
// every file that was inside it searchable.
func TestARemovedDirectoryReachesTheIndexAsASubtreeDeletion(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)
	h.Sweep(context.Background())
	h.awaitView(t, "the starting gap being closed", func(v View) bool { return v.Gap == nil })

	h.write(t, "pkg/a.go", "package pkg\n")
	h.awaitApply(t, service, "the new file", func(batches [][]codeintel.Change) bool {
		for _, batch := range batches {
			for _, change := range batch {
				if change.Path == "pkg/a.go" {
					return true
				}
			}
		}
		return false
	})

	if err := os.RemoveAll(filepath.Join(h.root, "pkg")); err != nil {
		t.Fatal(err)
	}
	h.awaitApply(t, service, "the subtree deletion", func(batches [][]codeintel.Change) bool {
		for _, batch := range batches {
			for _, change := range batch {
				if change.Path == "pkg" && change.Deleted && change.Subtree {
					return true
				}
			}
		}
		return false
	})
}

// A created directory carries no content of its own. Sending it would make the
// index read a path that is not a file and discard it, once per directory.
func TestACreatedDirectoryIsNotSentToTheIndex(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)
	h.Sweep(context.Background())
	h.awaitView(t, "the starting gap being closed", func(v View) bool { return v.Gap == nil })

	h.write(t, "pkg/a.go", "package pkg\n")
	h.awaitApply(t, service, "the new file", func(batches [][]codeintel.Change) bool {
		for _, batch := range batches {
			for _, change := range batch {
				if change.Path == "pkg/a.go" {
					return true
				}
			}
		}
		return false
	})

	for _, batch := range service.appliedPaths() {
		for _, change := range batch {
			if change.Path == "pkg" && !change.Deleted {
				t.Fatalf("a created directory was sent to the index: %+v", change)
			}
		}
	}
}

// A failed update must leave the work outstanding. Advancing the checkpoint over
// changes the index never received is the one way to lose them silently.
func TestAFailedUpdateDoesNotAdvanceTheCheckpoint(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)
	h.Sweep(context.Background())
	h.awaitView(t, "the starting gap being closed", func(v View) bool { return v.Gap == nil })

	service.setFailing(true)
	h.write(t, "main.go", "package main\n")

	view := h.awaitView(t, "the failure being reported", func(v View) bool { return v.LastError != "" })
	if view.Pending == 0 {
		t.Fatal("the change was dropped when the index refused it")
	}
	if view.Checkpoint == view.Sequence {
		t.Fatal("the checkpoint advanced over a change the index never received")
	}

	// Recovery is the next settle, not an operator's intervention.
	service.setFailing(false)
	h.Flush(context.Background(), h.project.ID)

	h.awaitView(t, "the retry succeeding", func(v View) bool {
		return v.Pending == 0 && v.LastError == "" && v.Checkpoint == v.Sequence
	})
}

// Flush is what lets a search see an edit that has not waited out its debounce
// window. Without it the answer would be correct about a generation the caller
// already knows is behind.
func TestFlushAppliesWithoutWaitingOutTheDebounceWindow(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)
	h.Sweep(context.Background())
	h.awaitView(t, "the starting gap being closed", func(v View) bool { return v.Gap == nil })

	// Recorded directly rather than through the filesystem, so the test is about
	// the flush and not about how quickly a native event arrives.
	journal := h.journalFor(t)
	journal.Record(fswatch.Event{Path: "urgent.go", Kind: fswatch.Written, At: h.clock.Now()})

	before := len(service.appliedPaths())
	h.Flush(context.Background(), h.project.ID)

	if len(service.appliedPaths()) <= before {
		t.Fatal("Flush returned without the change reaching the index")
	}
}

func TestFlushingAnUnwatchedProjectIsHarmless(t *testing.T) {
	service := newFakeService(t, []string{"."})
	h := newHarness(t, service, nil)

	h.Flush(context.Background(), h.project.ID)
	h.Flush(context.Background(), "no-such-project")
}

func TestIndexChangesTranslatesEveryObservation(t *testing.T) {
	entries := []changejournal.Entry{
		{Path: "a.go", Kind: fswatch.Written},
		{Path: "b.go", Kind: fswatch.Removed},
		{Path: "pkg", Kind: fswatch.Removed, Directory: true},
		{Path: "pkg", Kind: fswatch.Written, Directory: true},
	}
	got := indexChanges(entries)

	want := []codeintel.Change{
		{Path: "a.go"},
		{Path: "b.go", Deleted: true},
		{Path: "pkg", Deleted: true, Subtree: true},
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("changes[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
