package semantic

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/symbols"
)

func TestMemoryOptimisticLifecyclePersistenceAndHybridSearch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "semantic.db")
	source := &sourceStub{files: map[string][]byte{}}
	embedder := &deterministicEmbedder{dimension: 16}
	config := Config{Path: path, Model: "test", Dimensions: 16}
	store, err := Open(config, source, outlineStub{outline: symbols.Outline{Language: symbols.LanguageGo}}, embedder)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.MemoryPut(ctx, MemoryPutRequest{
		Key: "architecture/retry-policy", Kind: "decision",
		Content: "Retry transient requests with exponential backoff.", Confidence: 0.95,
		Provenance: []string{"docs/adr/retry.md"}, SourceCommit: "abc123", Reviewed: true,
	})
	if err != nil || record.Revision != 1 || record.ReviewDue || record.LowConfidence {
		t.Fatalf("created record = %+v, err = %v", record, err)
	}
	if _, err := store.MemoryPut(ctx, MemoryPutRequest{Key: record.Key, Kind: record.Kind,
		Content: "conflicting", Confidence: 1}); !hasCode(err, CodeMemoryConflict) {
		t.Fatalf("blind overwrite error = %v, want revision conflict", err)
	}
	expected := 1
	updated, err := store.MemoryPut(ctx, MemoryPutRequest{Key: record.Key, Kind: record.Kind,
		Content:    "Retry transient network requests with capped exponential backoff.",
		Confidence: 0.6, Provenance: record.Provenance, SourceCommit: "def456", ExpectedRevision: &expected})
	if err != nil || updated.Revision != 2 || !updated.LowConfidence {
		t.Fatalf("updated record = %+v, err = %v", updated, err)
	}
	search, err := store.MemorySearch(ctx, MemorySearchRequest{Query: "failed network request retry", Limit: 10})
	if err != nil || len(search.Matches) != 1 || search.Matches[0].Record.Key != record.Key || len(search.Modes) != 2 {
		t.Fatalf("search = %+v, err = %v", search, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config, source, outlineStub{outline: symbols.Outline{Language: symbols.LanguageGo}}, embedder)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.MemoryGet(ctx, MemoryGetRequest{Key: record.Key})
	if err != nil || persisted.Revision != 2 || persisted.SourceCommit != "def456" {
		t.Fatalf("persisted record = %+v, err = %v", persisted, err)
	}
	if err := reopened.MemoryDelete(ctx, MemoryDeleteRequest{Key: record.Key, ExpectedRevision: 1}); !hasCode(err, CodeMemoryConflict) {
		t.Fatalf("stale delete error = %v, want conflict", err)
	}
	if err := reopened.MemoryDelete(ctx, MemoryDeleteRequest{Key: record.Key, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.MemoryGet(ctx, MemoryGetRequest{Key: record.Key}); !hasCode(err, CodeMemoryNotFound) {
		t.Fatalf("get deleted error = %v, want not found", err)
	}
}

func TestMemoryRejectsEscapingProvenance(t *testing.T) {
	store := openTestStore(t, &sourceStub{files: map[string][]byte{}}, &deterministicEmbedder{dimension: 16})
	_, err := store.MemoryPut(context.Background(), MemoryPutRequest{Key: "bad", Kind: "fact",
		Content: "bad provenance", Confidence: 1, Provenance: []string{"../outside"}})
	if !hasCode(err, CodeInvalidQuery) {
		t.Fatalf("error = %v, want INVALID_QUERY", err)
	}
}

func TestConcurrentMemoryUpdatesPublishOneRevision(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, &sourceStub{files: map[string][]byte{}}, &deterministicEmbedder{dimension: 16})
	record, err := store.MemoryPut(ctx, MemoryPutRequest{Key: "concurrent", Kind: "fact", Content: "one", Confidence: 1})
	if err != nil {
		t.Fatal(err)
	}
	expected := record.Revision
	errorsByWriter := make(chan error, 2)
	for _, content := range []string{"writer-a", "writer-b"} {
		go func(content string) {
			_, err := store.MemoryPut(ctx, MemoryPutRequest{Key: "concurrent", Kind: "fact",
				Content: content, Confidence: 1, ExpectedRevision: &expected})
			errorsByWriter <- err
		}(content)
	}
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-errorsByWriter
		switch {
		case err == nil:
			succeeded++
		case hasCode(err, CodeMemoryConflict):
			conflicted++
		default:
			t.Fatalf("unexpected writer error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d, want one of each", succeeded, conflicted)
	}
	current, err := store.MemoryGet(ctx, MemoryGetRequest{Key: "concurrent"})
	if err != nil || current.Revision != 2 {
		t.Fatalf("current = %+v, err = %v", current, err)
	}
}

func TestMemoryStoresAreProjectIsolated(t *testing.T) {
	ctx := context.Background()
	source := &sourceStub{files: map[string][]byte{}}
	embedder := &deterministicEmbedder{dimension: 16}
	open := func(name string) *Store {
		store, err := Open(Config{Path: filepath.Join(t.TempDir(), name+".db"), Model: "test", Dimensions: 16},
			source, outlineStub{outline: symbols.Outline{Language: symbols.LanguageGo}}, embedder)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	alpha, beta := open("alpha"), open("beta")
	if _, err := alpha.MemoryPut(ctx, MemoryPutRequest{Key: "private", Kind: "fact", Content: "alpha only", Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := beta.MemoryGet(ctx, MemoryGetRequest{Key: "private"}); !hasCode(err, CodeMemoryNotFound) {
		t.Fatalf("beta read alpha memory: %v", err)
	}
}

func hasCode(err error, code string) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Code == code
}
