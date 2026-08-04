package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/searchindex"
)

// stubIndexer answers with whatever a test hands it. Only the index routes are
// exercised here, so the rest is present to satisfy the interface.
type stubIndexer struct {
	state   searchindex.State
	applied searchindex.Applied
}

func (s *stubIndexer) State() (searchindex.State, error) { return s.state, nil }

func (s *stubIndexer) Search(context.Context, searchindex.Request) (searchindex.Response, error) {
	return searchindex.Response{}, nil
}

func (s *stubIndexer) Rebuild(context.Context) (searchindex.State, error) { return s.state, nil }

func (s *stubIndexer) Reconcile(context.Context) (searchindex.State, searchindex.Applied, error) {
	return s.state, s.applied, nil
}

func (s *stubIndexer) Update(context.Context, []searchindex.Change) (searchindex.State, searchindex.Applied, error) {
	return s.state, s.applied, nil
}

func (s *stubIndexer) WatchSet(context.Context) ([]string, bool, error) { return nil, false, nil }

func (s *stubIndexer) DiskBytes() (int64, error) { return 0, nil }

func (s *stubIndexer) ReadProjectFile(string, int64) ([]byte, string, error) {
	return nil, "", nil
}

func postIndex(t *testing.T, indexer Indexer, body string) indexResponse {
	t.Helper()
	server := httptest.NewServer(New(indexer, nil, nil).Handler())
	t.Cleanup(server.Close)

	response, err := http.Post(server.URL+"/index", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /index: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	var decoded indexResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return decoded
}

// A batch that changed nothing returns the previously published state, and that
// state may well have been a full build. Reporting the state's flag would tell a
// caller a rebuild just happened when nothing did.
func TestABatchThatChangedNothingDoesNotReportARebuild(t *testing.T) {
	indexer := &stubIndexer{
		// The published index was last produced by a full build.
		state: searchindex.State{
			Generation: 7,
			FileCount:  120,
			FullBuild:  true,
			BuiltAt:    time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
		},
		// This request changed nothing and published no generation.
		applied: searchindex.Applied{Unchanged: 4},
	}

	got := postIndex(t, indexer, `{"mode":"apply","changes":[{"path":"a.go"}]}`)

	if got.FullBuild {
		t.Error("a batch that published no generation reported a full build")
	}
	if got.Applied != 0 {
		t.Errorf("applied = %d, want 0", got.Applied)
	}
	if got.Unchanged != 4 {
		t.Errorf("unchanged = %d, want 4", got.Unchanged)
	}
	if got.Generation != 7 {
		t.Errorf("generation = %d, want the published 7", got.Generation)
	}
}

// The count reported is what changed, not what was submitted. Those differ
// whenever a batch contains a save that edited nothing, which is the ordinary
// case rather than an unusual one.
func TestTheReportedCountIsWhatChangedRatherThanWhatWasSubmitted(t *testing.T) {
	indexer := &stubIndexer{
		state:   searchindex.State{Generation: 8, FileCount: 120},
		applied: searchindex.Applied{Changed: 1, Unchanged: 7, Generations: 1},
	}

	submitted := `{"mode":"apply","changes":[
		{"path":"a.go"},{"path":"b.go"},{"path":"c.go"},{"path":"d.go"},
		{"path":"e.go"},{"path":"f.go"},{"path":"g.go"},{"path":"h.go"}]}`

	got := postIndex(t, indexer, submitted)

	if got.Applied != 1 {
		t.Errorf("applied = %d for eight submitted paths with one real edit, want 1", got.Applied)
	}
	if got.Unchanged != 7 {
		t.Errorf("unchanged = %d, want 7", got.Unchanged)
	}
}

// An escalation is the request's own doing and is reported as such.
func TestAnEscalationReportsARebuild(t *testing.T) {
	indexer := &stubIndexer{
		state:   searchindex.State{Generation: 9, FileCount: 120, FullBuild: true},
		applied: searchindex.Applied{Changed: 120, Rebuilt: true, Generations: 1},
	}

	got := postIndex(t, indexer, `{"mode":"apply","changes":[{"path":"a.go"}]}`)

	if !got.FullBuild {
		t.Error("a batch that escalated to a full build did not report one")
	}
	if got.Applied != 120 {
		t.Errorf("applied = %d, want every indexed file", got.Applied)
	}
}
