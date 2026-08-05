package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/searchindex"
	"github.com/lev-goryachev/lctk/images/code-intel/internal/symbols"
)

type sourceStub struct {
	files map[string][]byte
}

func (s *sourceStub) ReadProjectFile(path string, _ int64) ([]byte, string, error) {
	content, found := s.files[path]
	if !found {
		return nil, "", errors.New("missing file")
	}
	sum := sha256.Sum256(content)
	return append([]byte(nil), content...), hex.EncodeToString(sum[:]), nil
}

type deterministicEmbedder struct {
	mu        sync.Mutex
	dimension int
	documents int
	fail      bool
}

func (e *deterministicEmbedder) Embed(_ context.Context, kind EmbeddingKind, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail {
		return nil, fail(CodeEmbeddingUnavailable, "inference stopped", true, nil)
	}
	if kind == EmbeddingDocument {
		e.documents += len(texts)
	}
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		vector := make([]float32, e.dimension)
		words := queryTerms(text)
		for _, word := range words {
			sum := sha256.Sum256([]byte(word))
			vector[int(sum[0])%len(vector)] += 1
		}
		if len(words) == 0 {
			vector[0] = 1
		}
		if err := normalize(vector); err != nil {
			return nil, err
		}
		vectors[i] = vector
	}
	return vectors, nil
}

func TestSyncPublishesAtomicallyAndReusesUnchangedChunks(t *testing.T) {
	ctx := context.Background()
	source := &sourceStub{files: map[string][]byte{
		"a.go": []byte("package a\n\nfunc Alpha() { helper() }\n"),
	}}
	embedder := &deterministicEmbedder{dimension: 16}
	store := openTestStore(t, source, embedder)
	first := exactState(1, source)
	status, err := store.Sync(ctx, first)
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if !status.Ready || status.Generation != 1 || status.ChunkCount == 0 {
		t.Fatalf("status = %+v, want published generation 1", status)
	}
	initialEmbeddings := embedder.documents

	status, err = store.Sync(ctx, exactState(2, source))
	if err != nil {
		t.Fatalf("unchanged Sync: %v", err)
	}
	if status.Generation != 2 {
		t.Fatalf("generation = %d, want 2", status.Generation)
	}
	if embedder.documents != initialEmbeddings {
		t.Fatalf("document embeddings = %d, want unchanged %d", embedder.documents, initialEmbeddings)
	}

	source.files["a.go"] = []byte("package a\n\nfunc Alpha() { changed() }\n")
	embedder.fail = true
	if _, err := store.Sync(ctx, exactState(3, source)); err == nil {
		t.Fatal("Sync succeeded while inference failed")
	}
	status, err = store.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Generation != 2 {
		t.Fatalf("failed Sync published generation %d, want prior generation 2", status.Generation)
	}
}

func TestHybridSearchRewardsSemanticAndLexicalAgreement(t *testing.T) {
	ctx := context.Background()
	source := &sourceStub{files: map[string][]byte{
		"retry.go": []byte("package retry\n\nfunc RetryFailedRequest() { backoff() }\n"),
		"cache.go": []byte("package cache\n\nfunc StoreResult() { persist() }\n"),
	}}
	embedder := &deterministicEmbedder{dimension: 16}
	store := openTestStore(t, source, embedder)
	if _, err := store.Sync(ctx, exactState(1, source)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	response, err := store.Search(ctx, Request{Query: "retry failed request", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Matches) != 1 || response.Matches[0].Path != "retry.go" {
		t.Fatalf("matches = %+v, want retry.go first", response.Matches)
	}
	match := response.Matches[0]
	if match.VectorRank == 0 || match.LexicalRank == 0 || match.HybridScore <= 0 {
		t.Fatalf("match = %+v, want transparent hybrid evidence", match)
	}
}

func TestOpeningWithAnotherModelFailsClosed(t *testing.T) {
	source := &sourceStub{files: map[string][]byte{}}
	embedder := &deterministicEmbedder{dimension: 16}
	path := filepath.Join(t.TempDir(), "semantic.db")
	first, err := Open(Config{Path: path, Model: "model-a", Dimensions: 16}, source, outlineStub{
		outline: symbols.Outline{Language: symbols.LanguageGo},
	}, embedder)
	if err != nil {
		t.Fatalf("Open first model: %v", err)
	}
	first.Close()
	_, err = Open(Config{Path: path, Model: "model-b", Dimensions: 16}, source, outlineStub{}, embedder)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeModelMismatch {
		t.Fatalf("Open second model error = %v, want MODEL_MISMATCH", err)
	}
}

func openTestStore(t *testing.T, source *sourceStub, embedder *deterministicEmbedder) *Store {
	t.Helper()
	store, err := Open(Config{
		Path: filepath.Join(t.TempDir(), "semantic.db"), Model: "test-model",
		Dimensions: embedder.dimension, BatchSize: 2,
	}, source, outlineStub{outline: symbols.Outline{Language: symbols.LanguageGo}}, embedder)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func exactState(generation uint64, source *sourceStub) searchindex.State {
	files := make(map[string]string, len(source.files))
	for path, content := range source.files {
		sum := sha256.Sum256(content)
		files[path] = hex.EncodeToString(sum[:])
	}
	return searchindex.State{
		SchemaVersion: searchindex.SchemaVersion, Generation: generation,
		Files: files, FileCount: len(files), BuiltAt: time.Now().UTC(),
	}
}

func TestNormalizeRejectsInvalidVectors(t *testing.T) {
	for _, vector := range [][]float32{{0, 0}, {float32(math.NaN()), 1}} {
		if err := normalize(vector); err == nil {
			t.Fatalf("normalize(%v) succeeded", vector)
		}
	}
	vector := []float32{3, 4}
	if err := normalize(vector); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var norm float64
	for _, value := range vector {
		norm += float64(value * value)
	}
	if math.Abs(math.Sqrt(norm)-1) > 1e-6 {
		t.Fatalf("normalized vector norm = %f, want 1", math.Sqrt(norm))
	}
}

func TestBoundedSemanticRankingKeepsExactTotal(t *testing.T) {
	source := &sourceStub{files: map[string][]byte{}}
	store := openTestStore(t, source, &deterministicEmbedder{dimension: 16})
	if err := store.PopulateStressCorpus(t.Context(), 200); err != nil {
		t.Fatalf("PopulateStressCorpus: %v", err)
	}
	response, err := store.Search(t.Context(), Request{Query: "synthetic stress symbol", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if response.Total != 200 || len(response.Matches) != 20 || !response.Truncated {
		t.Fatalf("response = %+v, want exact total 200 and bounded page 20", response)
	}
}
