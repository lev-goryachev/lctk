package gateway

import (
	"testing"
	"time"
)

// A project the host is not watching must not be reported as fresh. "Nobody
// looked" is not evidence that nothing changed, and an agent told "fresh" will
// not check again.
func TestAnUnwatchedProjectReportsUnknownFreshnessRatherThanFresh(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output := callProjectInfo(t, session, nil)

	if output.Index == nil {
		t.Fatal("project_info reported no index for a project with a running service")
	}
	if output.Index.Freshness != freshnessUnknown {
		t.Fatalf("freshness = %q, want %q for a project nothing is watching",
			output.Index.Freshness, freshnessUnknown)
	}
	if output.Changes != nil {
		t.Fatalf("changes = %+v, want nothing reported when no watcher exists", output.Changes)
	}
}

func TestAWatchedAndQuietProjectReportsFresh(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()
	f.changes["alpha-aaaaaaaa"] = ChangeState{Watching: true, DebounceSeconds: 3}

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output := callProjectInfo(t, session, nil)

	if output.Index.Freshness != freshnessFresh {
		t.Fatalf("freshness = %q, want %q", output.Index.Freshness, freshnessFresh)
	}
	if output.Changes == nil || !output.Changes.Watching || !output.Changes.Complete {
		t.Fatalf("changes = %+v, want a complete record from a live watcher", output.Changes)
	}
	if output.Changes.DebounceSeconds != 3 {
		t.Fatalf("debounce = %v, want the window a caller would have to wait out",
			output.Changes.DebounceSeconds)
	}
}

func TestPendingChangesMakeTheIndexStale(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()
	f.changes["alpha-aaaaaaaa"] = ChangeState{
		Watching:    true,
		Pending:     2,
		LastEventAt: testNow.Add(-time.Minute),
	}

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output := callProjectInfo(t, session, nil)

	if output.Index.Freshness != freshnessStale {
		t.Fatalf("freshness = %q, want %q with unapplied changes", output.Index.Freshness, freshnessStale)
	}
	if output.Changes.Pending != 2 {
		t.Fatalf("pending = %d, want 2", output.Changes.Pending)
	}
	if output.Changes.LastEventAt == "" {
		t.Error("the time of the last observed change was not reported")
	}
}

// A gap means the pending count is a lower bound. The answer has to say so, and
// must not read as current.
func TestAGapIsReportedAsAnIncompleteRecord(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	service := newFakeService(t)
	f.service["alpha-aaaaaaaa"] = service.address()
	f.changes["alpha-aaaaaaaa"] = ChangeState{Watching: true, GapReason: "watcher_overflow"}

	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])
	output := callProjectInfo(t, session, nil)

	if output.Changes.Complete {
		t.Fatal("an incomplete record was reported as complete")
	}
	if output.Changes.GapReason != "watcher_overflow" {
		t.Fatalf("gap_reason = %q, want the reason to reach the caller", output.Changes.GapReason)
	}
	if output.Index.Freshness != freshnessStale {
		t.Fatalf("freshness = %q, want %q when the record is incomplete",
			output.Index.Freshness, freshnessStale)
	}
}

// Using a project is what should make the host observe it. Verifying it here
// keeps the wakeup on the request path rather than on a timer.
func TestAnAcceptedRequestWakesTheHostWatcher(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")
	f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	if len(f.woken) == 0 {
		t.Fatal("an accepted request did not wake the project's watcher")
	}
	for _, id := range f.woken {
		if id != "alpha-aaaaaaaa" {
			t.Fatalf("woke %q, which is not the routed project", id)
		}
	}
}

func TestARefusedRequestDoesNotWakeAnything(t *testing.T) {
	f := newFixture(t, true, "alpha-aaaaaaaa")

	// A credential issued for another project must not even cause the routed
	// project to be observed: refusal happens before the project is in use.
	rawPost(t, f.server.URL+"/projects/alpha-aaaaaaaa/mcp", "not-a-grant")

	if len(f.woken) != 0 {
		t.Fatalf("a refused request woke %v", f.woken)
	}
}

func TestDescribeChangesIsPessimisticWhileIndexing(t *testing.T) {
	state := ChangeState{Watching: true}
	if _, freshness := describeChanges(state, true, true); freshness != freshnessUpdating {
		t.Fatalf("freshness = %q, want %q while a build is in flight", freshness, freshnessUpdating)
	}
	if _, freshness := describeChanges(ChangeState{}, false, true); freshness != freshnessUpdating {
		t.Fatalf("freshness = %q, want %q even with no watcher", freshness, freshnessUpdating)
	}
}
