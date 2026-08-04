package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/searchindex"
	"github.com/lev-goryachev/lctk/images/code-intel/internal/symbols"
)

// projectIndexer stands in for a store holding a handful of named files.
type projectIndexer struct {
	stubIndexer
	files     map[string]string
	candidate []string
	truncated bool
	// unreadable names files the index offers but the store refuses, which is the
	// ordinary race between an index and a disk.
	unreadable map[string]bool
	asked      []string
}

func (p *projectIndexer) FilesContainingWord(context.Context, string, int) ([]string, bool, error) {
	return p.candidate, p.truncated, nil
}

func (p *projectIndexer) ReadProjectFile(relative string, _ int64) ([]byte, string, error) {
	p.asked = append(p.asked, relative)
	if p.unreadable[relative] {
		return nil, "", &searchindex.Error{Code: searchindex.CodeFileNotFound, Message: "gone"}
	}
	content, ok := p.files[relative]
	if !ok {
		return nil, "", &searchindex.Error{Code: searchindex.CodeFileNotFound, Message: "gone"}
	}
	return []byte(content), "digest-" + relative, nil
}

func postLocate(t *testing.T, indexer Indexer, outliner Outliner, body string) LocateView {
	t.Helper()
	server := httptest.NewServer(New(indexer, outliner, nil).Handler())
	t.Cleanup(server.Close)

	response, err := http.Post(server.URL+"/locate", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /locate: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var view LocateView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return view
}

// The index narrows and the parser decides. A candidate whose only hit is prose
// contributes nothing to the answer while still being counted as considered, which
// is the whole point of the two-stage design.
func TestACandidateWhoseOnlyHitIsProseDropsOutButIsStillCounted(t *testing.T) {
	indexer := &projectIndexer{
		files: map[string]string{
			"real.go":    "package p\n\nfunc Needle() {}\n",
			"comment.go": "package p\n\n// Needle is only mentioned here.\nfunc Other() {}\n",
		},
		candidate: []string{"comment.go", "real.go"},
	}

	view := postLocate(t, indexer, newOutliner(t), `{"name":"Needle"}`)

	if view.FilesConsidered != 2 {
		t.Errorf("files_considered = %d, want 2", view.FilesConsidered)
	}
	if len(view.Files) != 1 || view.Files[0].Path != "real.go" {
		t.Fatalf("files = %+v", view.Files)
	}
	if view.Occurrences != 1 || view.Declarations != 1 {
		t.Errorf("occurrences = %d, declarations = %d", view.Occurrences, view.Declarations)
	}
	// Every candidate is read, including the one that contributes nothing: the
	// decision requires the content.
	if len(indexer.asked) != 2 {
		t.Errorf("the store was asked for %v", indexer.asked)
	}
}

func TestDeclarationsOnlyKeepsTheDefinitionAndDropsTheUses(t *testing.T) {
	indexer := &projectIndexer{
		files: map[string]string{
			"decl.go": "package p\n\nfunc Needle() {}\n",
			"use.go":  "package p\n\nfunc Other() { Needle() }\n",
		},
		candidate: []string{"decl.go", "use.go"},
	}

	all := postLocate(t, indexer, newOutliner(t), `{"name":"Needle"}`)
	if all.Occurrences != 2 || len(all.Files) != 2 {
		t.Fatalf("the unfiltered lookup found %d occurrences across %d files", all.Occurrences, len(all.Files))
	}

	only := postLocate(t, indexer, newOutliner(t), `{"name":"Needle","declarations_only":true}`)
	if len(only.Files) != 1 || only.Files[0].Path != "decl.go" {
		t.Errorf("files = %+v, want only the declaring file", only.Files)
	}
	if only.Occurrences != 1 {
		t.Errorf("occurrences = %d, want 1", only.Occurrences)
	}
}

// A file the index offered but the store will not read is counted, never allowed
// to fail the lookup. One awkward file must not deny the answer about the rest.
func TestAnUnreadableCandidateIsCounted(t *testing.T) {
	indexer := &projectIndexer{
		files:      map[string]string{"real.go": "package p\n\nfunc Needle() {}\n"},
		candidate:  []string{"gone.go", "real.go"},
		unreadable: map[string]bool{"gone.go": true},
	}

	view := postLocate(t, indexer, newOutliner(t), `{"name":"Needle"}`)

	if view.SkippedUnreadable != 1 {
		t.Errorf("skipped_unreadable = %d, want 1", view.SkippedUnreadable)
	}
	if view.Occurrences != 1 {
		t.Errorf("occurrences = %d; one unreadable file denied the rest of the answer", view.Occurrences)
	}
}

// A candidate in a language this build has no grammar for is counted separately,
// because "we did not look" and "we looked and found nothing" are different claims.
func TestACandidateInAnUnsupportedLanguageIsCountedSeparately(t *testing.T) {
	indexer := &projectIndexer{
		files: map[string]string{
			"real.go":   "package p\n\nfunc Needle() {}\n",
			"README.md": "# Needle\n",
		},
		candidate: []string{"README.md", "real.go"},
	}

	view := postLocate(t, indexer, newOutliner(t), `{"name":"Needle"}`)

	if view.SkippedUnsupported != 1 {
		t.Errorf("skipped_unsupported = %d, want 1", view.SkippedUnsupported)
	}
	if view.SkippedUnreadable != 0 {
		t.Errorf("an unsupported language was counted as unreadable: %+v", view)
	}
	if view.Occurrences != 1 {
		t.Errorf("occurrences = %d", view.Occurrences)
	}
}

func TestTruncationSurvivesFromTheIndexToTheAnswer(t *testing.T) {
	// A caller that reads a truncated answer as complete concludes "nothing else
	// refers to this", which is the one wrong conclusion available here.
	indexer := &projectIndexer{
		files:     map[string]string{"real.go": "package p\n\nfunc Needle() {}\n"},
		candidate: []string{"real.go"},
		truncated: true,
	}

	view := postLocate(t, indexer, newOutliner(t), `{"name":"Needle"}`)
	if !view.FilesTruncated {
		t.Error("truncation was lost between the index and the answer")
	}
}

func TestTheAnswerNamesTheGenerationThatChoseTheFiles(t *testing.T) {
	// Unlike an outline, this answer is only as complete as the index that narrowed
	// it, so the generation belongs in it.
	indexer := &projectIndexer{
		files:     map[string]string{"real.go": "package p\n\nfunc Needle() {}\n"},
		candidate: []string{"real.go"},
	}
	indexer.state = searchindex.State{Generation: 42}

	view := postLocate(t, indexer, newOutliner(t), `{"name":"Needle"}`)
	if view.Generation != 42 {
		t.Errorf("generation = %d, want 42", view.Generation)
	}
}

func TestABuildWithoutASymbolEngineRefusesALookup(t *testing.T) {
	server := httptest.NewServer(New(&projectIndexer{}, nil, nil).Handler())
	t.Cleanup(server.Close)

	response, err := http.Post(server.URL+"/locate", "application/json", strings.NewReader(`{"name":"Needle"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	var envelope errorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != symbols.CodeUnsupportedLanguage {
		t.Errorf("code = %q", envelope.Error.Code)
	}
}
