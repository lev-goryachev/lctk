package semantic

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/searchindex"
	_ "github.com/ncruces/go-sqlite3/driver"
)

const schemaVersion = 1

// Source is the exact index's scoped reader. The semantic store deliberately
// lacks a filesystem root, so only files accepted into the exact generation can
// become semantic state.
type Source interface {
	ReadProjectFile(relative string, maxBytes int64) ([]byte, string, error)
}

// Config fixes the persistent compatibility contract for one store.
type Config struct {
	Path         string
	Model        string
	Dimensions   int
	BatchSize    int
	MaxFileBytes int64
}

// Store owns one project's semantic database and its atomic publication lock.
type Store struct {
	db         *sql.DB
	source     Source
	chunker    Chunker
	embedder   Embedder
	config     Config
	mu         sync.Mutex
	progressMu sync.RWMutex
	progress   syncProgress
}

type syncProgress struct {
	running  bool
	total    int
	embedded int
	reused   int
}

// Status states exactly which exact-index generation the semantic database
// describes. A caller compares Generation with the exact generation instead of
// inferring freshness from timestamps.
type Status struct {
	Ready          bool   `json:"ready"`
	Generation     uint64 `json:"generation"`
	FileCount      int    `json:"file_count"`
	ChunkCount     int    `json:"chunk_count"`
	Model          string `json:"model,omitempty"`
	Dimensions     int    `json:"dimensions,omitempty"`
	IndexedAt      string `json:"indexed_at,omitempty"`
	Reason         string `json:"reason,omitempty"`
	ChunksTotal    int    `json:"chunks_total,omitempty"`
	ChunksEmbedded int    `json:"chunks_embedded,omitempty"`
	ChunksReused   int    `json:"chunks_reused,omitempty"`
}

// Open validates or creates one project's database. It fails closed on a newer
// schema or a model mismatch; silently rebuilding either would defeat Stage 7's
// update and rollback boundary.
func Open(config Config, source Source, outliner Outliner, embedder Embedder) (*Store, error) {
	if config.Path == "" || config.Model == "" || config.Dimensions <= 0 || source == nil || embedder == nil {
		return nil, fail(CodeInternalError, "The semantic store configuration is incomplete.", false, nil)
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 32
	}
	if config.MaxFileBytes <= 0 {
		config.MaxFileBytes = 4 << 20
	}
	if err := os.MkdirAll(filepath.Dir(config.Path), 0o755); err != nil {
		return nil, fail(CodeInternalError, "The semantic state directory could not be created.", false, err)
	}
	// The ncruces driver embeds SQLite's cross-platform WASM runtime. PRAGMAs live
	// in the DSN so every connection receives the same durability and integrity
	// policy.
	dsn := "file:" + filepath.ToSlash(config.Path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fail(CodeInternalError, "The semantic database could not be opened.", false, err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{
		db: db, source: source, embedder: embedder, config: config,
		chunker: Chunker{Outliner: outliner},
	}
	if err := store.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close flushes SQLite and releases the project database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(CodeCorrupt, "The semantic schema version could not be read.", false, err)
	}
	if version > schemaVersion {
		return fail(CodeCorrupt, "The semantic database was written by a newer LCTK version.", false, nil)
	}
	if version != 0 && version != schemaVersion {
		return fail(CodeCorrupt, "The semantic database requires an explicit migration.", false, nil)
	}
	schema := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS semantic_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS semantic_files (
    path TEXT PRIMARY KEY,
    digest TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS semantic_chunks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    stable_id TEXT NOT NULL UNIQUE,
    path TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    language TEXT NOT NULL,
    precision TEXT NOT NULL,
    anchor TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    content TEXT NOT NULL,
    embedding_text TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS semantic_chunks_path ON semantic_chunks(path);
CREATE TABLE IF NOT EXISTS semantic_vectors (
    rowid INTEGER PRIMARY KEY,
    embedding BLOB NOT NULL
);
PRAGMA user_version = %d;`, schemaVersion)
	if _, err := s.db.Exec(schema); err != nil {
		return fail(CodeCorrupt, "The semantic schema could not be initialized.", false, err)
	}
	if err := s.requireCompatibility("model", s.config.Model); err != nil {
		return err
	}
	if err := s.requireCompatibility("dimensions", strconv.Itoa(s.config.Dimensions)); err != nil {
		return err
	}
	return nil
}

func (s *Store) requireCompatibility(key, wanted string) error {
	var current string
	err := s.db.QueryRow("SELECT value FROM semantic_meta WHERE key = ?", key).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec("INSERT INTO semantic_meta(key, value) VALUES(?, ?)", key, wanted); err != nil {
			return fail(CodeCorrupt, "The semantic compatibility metadata could not be written.", false, err)
		}
		return nil
	case err != nil:
		return fail(CodeCorrupt, "The semantic compatibility metadata could not be read.", false, err)
	case current != wanted:
		return fail(CodeModelMismatch,
			fmt.Sprintf("The semantic database uses %s %q, but this service requires %q.", key, current, wanted),
			false, nil)
	default:
		return nil
	}
}

// Sync brings semantic state to one already-published exact generation. All
// inference finishes before the transaction; a failed batch therefore leaves
// the last complete semantic generation queryable and visibly stale.
func (s *Store) Sync(ctx context.Context, exact searchindex.State) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setProgress(syncProgress{running: true})
	defer func() {
		s.progressMu.Lock()
		s.progress.running = false
		s.progressMu.Unlock()
	}()

	knownFiles, err := s.fileDigests(ctx)
	if err != nil {
		return Status{}, err
	}
	changed := make([]string, 0)
	for path, digest := range exact.Files {
		if knownFiles[path] != digest {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	deleted := make([]string, 0)
	for path := range knownFiles {
		if _, exists := exact.Files[path]; !exists {
			deleted = append(deleted, path)
		}
	}
	sort.Strings(deleted)

	prepared := make(map[string][]preparedChunk, len(changed))
	type embeddingWork struct {
		path  string
		index int
		text  string
	}
	var work []embeddingWork
	reused := 0
	for _, path := range changed {
		content, digest, err := s.source.ReadProjectFile(path, s.config.MaxFileBytes)
		if err != nil {
			return Status{}, fail(CodeInternalError,
				"A file accepted by the exact generation could not be read for semantic indexing.", false, err)
		}
		if digest != exact.Files[path] {
			return Status{}, fail(CodeNotReady,
				"A source file changed after the exact generation was published; reconcile it before semantic indexing.", true, nil)
		}
		chunks, err := s.chunker.Chunks(ctx, path, content, digest)
		if err != nil {
			return Status{}, err
		}
		existing, err := s.chunksForPath(ctx, path)
		if err != nil {
			return Status{}, err
		}
		items := make([]preparedChunk, len(chunks))
		for i, chunk := range chunks {
			items[i] = preparedChunk{Chunk: chunk}
			if row, found := existing[chunk.StableID]; found && row.ContentDigest == chunk.ContentDigest {
				items[i].ExistingID = row.ID
				reused++
				continue
			}
			work = append(work, embeddingWork{path: path, index: i, text: chunk.EmbeddingText})
		}
		prepared[path] = items
	}
	s.setProgress(syncProgress{running: true, total: len(work) + reused, reused: reused})
	for start := 0; start < len(work); start += s.config.BatchSize {
		end := start + s.config.BatchSize
		if end > len(work) {
			end = len(work)
		}
		texts := make([]string, end-start)
		for i := start; i < end; i++ {
			texts[i-start] = work[i].text
		}
		vectors, err := s.embedder.Embed(ctx, EmbeddingDocument, texts)
		if err != nil {
			return Status{}, err
		}
		for i, vector := range vectors {
			item := work[start+i]
			prepared[item.path][item.index].Vector = vector
		}
		s.setProgress(syncProgress{running: true, total: len(work) + reused,
			embedded: end, reused: reused})
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Status{}, fail(CodeInternalError, "The semantic publication could not start.", false, err)
	}
	defer tx.Rollback()
	for _, path := range deleted {
		if err := deletePath(ctx, tx, path); err != nil {
			return Status{}, err
		}
	}
	for _, path := range changed {
		if err := s.replacePath(ctx, tx, path, exact.Files[path], prepared[path]); err != nil {
			return Status{}, err
		}
	}
	metadata := map[string]string{
		"generation": strconv.FormatUint(exact.Generation, 10),
		"indexed_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	for key, value := range metadata {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO semantic_meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
			key, value); err != nil {
			return Status{}, fail(CodeInternalError, "Semantic publication metadata could not be written.", false, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Status{}, fail(CodeInternalError, "The semantic generation could not be committed.", false, err)
	}
	return s.Status()
}

type existingChunk struct {
	ID            int64
	ContentDigest string
}

type preparedChunk struct {
	Chunk
	ExistingID int64
	Vector     []float32
}

func (s *Store) fileDigests(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT path, digest FROM semantic_files")
	if err != nil {
		return nil, fail(CodeCorrupt, "Semantic file state could not be read.", false, err)
	}
	defer rows.Close()
	values := make(map[string]string)
	for rows.Next() {
		var path, digest string
		if err := rows.Scan(&path, &digest); err != nil {
			return nil, fail(CodeCorrupt, "Semantic file state is invalid.", false, err)
		}
		values[path] = digest
	}
	if err := rows.Err(); err != nil {
		return nil, fail(CodeCorrupt, "Semantic file state could not be completed.", false, err)
	}
	return values, nil
}

func (s *Store) chunksForPath(ctx context.Context, path string) (map[string]existingChunk, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, stable_id, content_digest FROM semantic_chunks WHERE path = ?", path)
	if err != nil {
		return nil, fail(CodeCorrupt, "Existing semantic chunks could not be read.", false, err)
	}
	defer rows.Close()
	values := make(map[string]existingChunk)
	for rows.Next() {
		var row existingChunk
		var stable string
		if err := rows.Scan(&row.ID, &stable, &row.ContentDigest); err != nil {
			return nil, fail(CodeCorrupt, "Existing semantic chunk metadata is invalid.", false, err)
		}
		values[stable] = row
	}
	return values, rows.Err()
}

func deletePath(ctx context.Context, tx *sql.Tx, path string) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM semantic_chunks WHERE path = ?", path)
	if err != nil {
		return fail(CodeInternalError, "Old semantic chunk identifiers could not be read.", false, err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fail(CodeInternalError, "An old semantic chunk identifier is invalid.", false, err)
		}
		ids = append(ids, id)
	}
	err = rows.Close()
	if err != nil {
		return fail(CodeInternalError, "Old semantic chunks could not be enumerated.", false, err)
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "DELETE FROM semantic_vectors WHERE rowid = ?", id); err != nil {
			return fail(CodeInternalError, "An old semantic vector could not be deleted.", false, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM semantic_chunks WHERE path = ?", path); err != nil {
		return fail(CodeInternalError, "Old semantic chunk metadata could not be deleted.", false, err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM semantic_files WHERE path = ?", path); err != nil {
		return fail(CodeInternalError, "Old semantic file metadata could not be deleted.", false, err)
	}
	return nil
}

func (s *Store) replacePath(ctx context.Context, tx *sql.Tx, path, digest string, chunks []preparedChunk) error {
	existingRows, err := tx.QueryContext(ctx, "SELECT id, stable_id FROM semantic_chunks WHERE path = ?", path)
	if err != nil {
		return fail(CodeInternalError, "Existing semantic chunks could not be prepared for replacement.", false, err)
	}
	existing := make(map[string]int64)
	for existingRows.Next() {
		var id int64
		var stable string
		if err := existingRows.Scan(&id, &stable); err != nil {
			existingRows.Close()
			return fail(CodeInternalError, "Existing semantic chunk metadata is invalid.", false, err)
		}
		existing[stable] = id
	}
	if err := existingRows.Close(); err != nil {
		return fail(CodeInternalError, "Existing semantic chunks could not be enumerated.", false, err)
	}
	keep := make(map[string]bool, len(chunks))
	for _, item := range chunks {
		keep[item.StableID] = item.ExistingID != 0
	}
	for stable, id := range existing {
		if keep[stable] {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM semantic_vectors WHERE rowid = ?", id); err != nil {
			return fail(CodeInternalError, "A replaced semantic vector could not be deleted.", false, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM semantic_chunks WHERE id = ?", id); err != nil {
			return fail(CodeInternalError, "Replaced semantic chunk metadata could not be deleted.", false, err)
		}
	}
	for _, item := range chunks {
		if item.ExistingID != 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE semantic_chunks SET
path=?, language=?, precision=?, anchor=?, ordinal=?, start_line=?, end_line=?, content=?, embedding_text=? WHERE id=?`,
				item.Path, item.Language, item.Precision, item.Anchor, item.Ordinal,
				item.StartLine, item.EndLine, item.Content, item.EmbeddingText, item.ExistingID); err != nil {
				return fail(CodeInternalError, "Reusable semantic chunk metadata could not be updated.", false, err)
			}
			continue
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO semantic_chunks(
stable_id, path, content_digest, language, precision, anchor, ordinal, start_line, end_line, content, embedding_text)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.StableID, item.Path, item.ContentDigest, item.Language, item.Precision,
			item.Anchor, item.Ordinal, item.StartLine, item.EndLine, item.Content, item.EmbeddingText)
		if err != nil {
			return fail(CodeInternalError, "Semantic chunk metadata could not be inserted.", false, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fail(CodeInternalError, "The semantic chunk identifier could not be recovered.", false, err)
		}
		blob, err := serializeVector(item.Vector, s.config.Dimensions)
		if err != nil {
			return fail(CodeInternalError, "A semantic vector could not be serialized.", false, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO semantic_vectors(rowid, embedding) VALUES(?, ?)", id, blob); err != nil {
			return fail(CodeInternalError, "A semantic vector could not be inserted.", false, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO semantic_files(path, digest) VALUES(?, ?) ON CONFLICT(path) DO UPDATE SET digest=excluded.digest",
		path, digest); err != nil {
		return fail(CodeInternalError, "Semantic file metadata could not be published.", false, err)
	}
	return nil
}

// serializeVector gives the owned adapter a stable little-endian disk format.
// The dimension check keeps a malformed embedder response from corrupting a
// whole generation during publication.
func serializeVector(vector []float32, dimensions int) ([]byte, error) {
	if len(vector) != dimensions {
		return nil, fail(CodeModelMismatch,
			fmt.Sprintf("An embedding has %d dimensions; %d are required.", len(vector), dimensions), false, nil)
	}
	encoded := make([]byte, len(vector)*4)
	for i, value := range vector {
		binary.LittleEndian.PutUint32(encoded[i*4:], math.Float32bits(value))
	}
	return encoded, nil
}

// Status reads the one committed publication boundary.
func (s *Store) Status() (Status, error) {
	status := Status{Model: s.config.Model, Dimensions: s.config.Dimensions}
	s.progressMu.RLock()
	status.ChunksTotal = s.progress.total
	status.ChunksEmbedded = s.progress.embedded
	status.ChunksReused = s.progress.reused
	s.progressMu.RUnlock()
	var generation, indexedAt string
	if err := s.db.QueryRow("SELECT value FROM semantic_meta WHERE key='generation'").Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			status.Reason = "The semantic index has not been built yet."
			return status, nil
		}
		return Status{}, fail(CodeCorrupt, "Semantic publication metadata could not be read.", false, err)
	}
	value, err := strconv.ParseUint(generation, 10, 64)
	if err != nil {
		return Status{}, fail(CodeCorrupt, "The semantic generation is invalid.", false, err)
	}
	if err := s.db.QueryRow("SELECT value FROM semantic_meta WHERE key='indexed_at'").Scan(&indexedAt); err != nil {
		return Status{}, fail(CodeCorrupt, "The semantic publication time could not be read.", false, err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM semantic_files").Scan(&status.FileCount); err != nil {
		return Status{}, fail(CodeCorrupt, "The semantic file count could not be read.", false, err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM semantic_chunks").Scan(&status.ChunkCount); err != nil {
		return Status{}, fail(CodeCorrupt, "The semantic chunk count could not be read.", false, err)
	}
	status.Ready = true
	status.Generation = value
	status.IndexedAt = indexedAt
	return status, nil
}

func (s *Store) setProgress(progress syncProgress) {
	s.progressMu.Lock()
	s.progress = progress
	s.progressMu.Unlock()
}
