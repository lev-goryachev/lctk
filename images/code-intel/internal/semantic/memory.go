package semantic

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maximumMemoryContent = 64 << 10
	memoryReviewWindow   = 90 * 24 * time.Hour
)

var memoryKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,199}$`)

// MemoryRecord is explicit project knowledge. ReviewDue and LowConfidence label
// risk instead of hiding the record, preserving the review contract in ADR-0021.
type MemoryRecord struct {
	Key           string   `json:"key"`
	Kind          string   `json:"kind"`
	Content       string   `json:"content"`
	Confidence    float64  `json:"confidence"`
	Provenance    []string `json:"provenance"`
	SourceCommit  string   `json:"source_commit,omitempty"`
	Revision      int      `json:"revision"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	ReviewedAt    string   `json:"reviewed_at,omitempty"`
	ReviewDue     bool     `json:"review_due"`
	LowConfidence bool     `json:"low_confidence"`
}

// MemoryPutRequest creates a new key or updates exactly the expected revision.
// ExpectedRevision is omitted only for create; an existing key then conflicts.
type MemoryPutRequest struct {
	Key              string   `json:"key"`
	Kind             string   `json:"kind"`
	Content          string   `json:"content"`
	Confidence       float64  `json:"confidence"`
	Provenance       []string `json:"provenance,omitempty"`
	SourceCommit     string   `json:"source_commit,omitempty"`
	ExpectedRevision *int     `json:"expected_revision,omitempty"`
	Reviewed         bool     `json:"reviewed,omitempty"`
}

// MemoryGetRequest names one stable key.
type MemoryGetRequest struct {
	Key string `json:"key"`
}

// MemoryDeleteRequest requires the revision the client actually read.
type MemoryDeleteRequest struct {
	Key              string `json:"key"`
	ExpectedRevision int    `json:"expected_revision"`
}

// MemorySearchRequest supports an empty query as the bounded list operation.
type MemorySearchRequest struct {
	Query string `json:"query,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// MemoryMatch carries transparent lexical/vector ranks beside the record.
type MemoryMatch struct {
	Record       MemoryRecord `json:"record"`
	VectorScore  float64      `json:"vector_score,omitempty"`
	LexicalScore float64      `json:"lexical_score,omitempty"`
	HybridScore  float64      `json:"hybrid_score,omitempty"`
	VectorRank   int          `json:"vector_rank,omitempty"`
	LexicalRank  int          `json:"lexical_rank,omitempty"`
}

// MemorySearchResponse states whether listing, lexical, and semantic modes took
// part, so unavailable semantic retrieval can never masquerade as a full answer.
type MemorySearchResponse struct {
	Matches    []MemoryMatch `json:"matches"`
	Total      int           `json:"total"`
	Truncated  bool          `json:"truncated"`
	Modes      []string      `json:"modes"`
	Model      string        `json:"model,omitempty"`
	Dimensions int           `json:"dimensions,omitempty"`
}

// MemoryPut validates and embeds before opening the write transaction. A failed
// inference request therefore cannot create a metadata-only record.
func (s *Store) MemoryPut(ctx context.Context, request MemoryPutRequest) (MemoryRecord, error) {
	key, kind, provenance, err := validateMemory(request.Key, request.Kind, request.Content, request.Confidence, request.Provenance)
	if err != nil {
		return MemoryRecord{}, err
	}
	if len(request.SourceCommit) > 128 {
		return MemoryRecord{}, fail(CodeInvalidQuery, "source_commit exceeds 128 bytes.", false, nil)
	}
	vectors, err := s.embedder.Embed(ctx, EmbeddingDocument, []string{memoryEmbeddingText(key, kind, request.Content, provenance)})
	if err != nil {
		return MemoryRecord{}, err
	}
	encoded, err := serializeVector(vectors[0], s.config.Dimensions)
	if err != nil {
		return MemoryRecord{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	provenanceJSON, _ := json.Marshal(provenance)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryRecord{}, fail(CodeInternalError, "The memory transaction could not start.", false, err)
	}
	defer tx.Rollback()
	var currentRevision int
	var createdAt, reviewedAt string
	err = tx.QueryRowContext(ctx, "SELECT revision,created_at,reviewed_at FROM memory_records WHERE key=?", key).Scan(&currentRevision, &createdAt, &reviewedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if request.ExpectedRevision != nil && *request.ExpectedRevision != 0 {
			return MemoryRecord{}, fail(CodeMemoryConflict, "The memory key does not exist at the expected revision.", false, nil)
		}
		currentRevision, createdAt = 0, now
	case err != nil:
		return MemoryRecord{}, fail(CodeCorrupt, "Existing memory metadata could not be read.", false, err)
	case request.ExpectedRevision == nil || *request.ExpectedRevision != currentRevision:
		return MemoryRecord{}, fail(CodeMemoryConflict,
			fmt.Sprintf("The memory key is revision %d; read it and retry with that revision.", currentRevision), false, nil)
	}
	if request.Reviewed {
		reviewedAt = now
	}
	revision := currentRevision + 1
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_records(
key,kind,content,confidence,provenance_json,source_commit,revision,created_at,updated_at,reviewed_at,embedding)
VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET
kind=excluded.kind,content=excluded.content,confidence=excluded.confidence,
provenance_json=excluded.provenance_json,source_commit=excluded.source_commit,
revision=excluded.revision,updated_at=excluded.updated_at,reviewed_at=excluded.reviewed_at,
embedding=excluded.embedding`, key, kind, strings.TrimSpace(request.Content), request.Confidence,
		string(provenanceJSON), strings.TrimSpace(request.SourceCommit), revision, createdAt, now, reviewedAt, encoded)
	if err != nil {
		return MemoryRecord{}, fail(CodeInternalError, "The memory record could not be written.", false, err)
	}
	if err := tx.Commit(); err != nil {
		return MemoryRecord{}, fail(CodeInternalError, "The memory record could not be committed.", false, err)
	}
	return s.MemoryGet(ctx, MemoryGetRequest{Key: key})
}

// MemoryGet reads one explicit key without invoking inference.
func (s *Store) MemoryGet(ctx context.Context, request MemoryGetRequest) (MemoryRecord, error) {
	key := strings.TrimSpace(request.Key)
	if !memoryKeyPattern.MatchString(key) {
		return MemoryRecord{}, fail(CodeInvalidQuery, "The memory key is invalid.", false, nil)
	}
	record, _, err := s.readMemoryRow(s.db.QueryRowContext(ctx, `SELECT key,kind,content,confidence,
provenance_json,source_commit,revision,created_at,updated_at,reviewed_at,embedding FROM memory_records WHERE key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoryRecord{}, fail(CodeMemoryNotFound, "The memory key does not exist.", false, nil)
	}
	if err != nil {
		return MemoryRecord{}, fail(CodeCorrupt, "The memory record could not be read.", false, err)
	}
	return record, nil
}

// MemoryDelete removes exactly the revision the client read.
func (s *Store) MemoryDelete(ctx context.Context, request MemoryDeleteRequest) error {
	key := strings.TrimSpace(request.Key)
	if !memoryKeyPattern.MatchString(key) || request.ExpectedRevision < 1 {
		return fail(CodeInvalidQuery, "A valid key and positive expected_revision are required.", false, nil)
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM memory_records WHERE key=? AND revision=?", key, request.ExpectedRevision)
	if err != nil {
		return fail(CodeInternalError, "The memory record could not be deleted.", false, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fail(CodeInternalError, "The memory deletion result could not be read.", false, err)
	}
	if count == 0 {
		var exists int
		err := s.db.QueryRowContext(ctx, "SELECT 1 FROM memory_records WHERE key=?", key).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return fail(CodeMemoryNotFound, "The memory key does not exist.", false, nil)
		}
		return fail(CodeMemoryConflict, "The memory revision changed; read it before deleting.", false, err)
	}
	return nil
}

type memoryRowScanner interface{ Scan(...any) error }

func (s *Store) readMemoryRow(scanner memoryRowScanner) (MemoryRecord, []float32, error) {
	var record MemoryRecord
	var provenanceJSON string
	var encoded []byte
	if err := scanner.Scan(&record.Key, &record.Kind, &record.Content, &record.Confidence,
		&provenanceJSON, &record.SourceCommit, &record.Revision, &record.CreatedAt,
		&record.UpdatedAt, &record.ReviewedAt, &encoded); err != nil {
		return MemoryRecord{}, nil, err
	}
	if err := json.Unmarshal([]byte(provenanceJSON), &record.Provenance); err != nil {
		return MemoryRecord{}, nil, err
	}
	vector, err := deserializeVector(encoded, s.config.Dimensions)
	if err != nil {
		return MemoryRecord{}, nil, err
	}
	record.LowConfidence = record.Confidence < 0.7
	reviewTime, parseErr := time.Parse(time.RFC3339Nano, record.ReviewedAt)
	record.ReviewDue = record.ReviewedAt == "" || parseErr != nil || time.Since(reviewTime) > memoryReviewWindow
	return record, vector, nil
}

// MemorySearch performs deterministic hybrid ranking; an empty query is the
// documented list operation and needs no embedding service call.
func (s *Store) MemorySearch(ctx context.Context, request MemorySearchRequest) (MemorySearchResponse, error) {
	query := strings.TrimSpace(request.Query)
	if len(query) > 8192 {
		return MemorySearchResponse{}, fail(CodeInvalidQuery, "The memory query exceeds 8192 bytes.", false, nil)
	}
	if request.Kind != "" && !validMemoryKind(request.Kind) {
		return MemorySearchResponse{}, fail(CodeInvalidQuery, "The memory kind is invalid.", false, nil)
	}
	limit := request.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return MemorySearchResponse{}, fail(CodeInvalidQuery, "The memory result limit must be between 1 and 200.", false, nil)
	}
	statement := `SELECT key,kind,content,confidence,provenance_json,source_commit,revision,
created_at,updated_at,reviewed_at,embedding FROM memory_records`
	var args []any
	if request.Kind != "" {
		statement += " WHERE kind=?"
		args = append(args, request.Kind)
	}
	statement += " ORDER BY updated_at DESC,key"
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return MemorySearchResponse{}, fail(CodeCorrupt, "Memory records could not be searched.", false, err)
	}
	defer rows.Close()
	type candidate struct {
		match   MemoryMatch
		vector  []float32
		lexical float64
	}
	var candidates []candidate
	for rows.Next() {
		record, vector, err := s.readMemoryRow(rows)
		if err != nil {
			return MemorySearchResponse{}, fail(CodeCorrupt, "A memory record is invalid.", false, err)
		}
		candidates = append(candidates, candidate{match: MemoryMatch{Record: record}, vector: vector})
	}
	if err := rows.Err(); err != nil {
		return MemorySearchResponse{}, err
	}
	if query == "" {
		total := len(candidates)
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
		matches := make([]MemoryMatch, len(candidates))
		for i := range candidates {
			matches[i] = candidates[i].match
		}
		return MemorySearchResponse{Matches: matches, Total: total, Truncated: total > len(matches), Modes: []string{"list"}}, nil
	}
	vectors, err := s.embedder.Embed(ctx, EmbeddingQuery, []string{query})
	if err != nil {
		return MemorySearchResponse{}, err
	}
	terms := queryTerms(query)
	vectorOrder := make([]int, len(candidates))
	lexicalOrder := make([]int, 0, len(candidates))
	for i := range candidates {
		vectorOrder[i] = i
		candidates[i].match.VectorScore = cosine(vectors[0], candidates[i].vector)
		haystack := strings.ToLower(candidates[i].match.Record.Key + " " + candidates[i].match.Record.Kind + " " + candidates[i].match.Record.Content)
		for _, term := range terms {
			candidates[i].lexical += float64(strings.Count(haystack, term))
		}
		if candidates[i].lexical > 0 {
			lexicalOrder = append(lexicalOrder, i)
		}
	}
	sort.Slice(vectorOrder, func(i, j int) bool {
		a, b := candidates[vectorOrder[i]], candidates[vectorOrder[j]]
		if a.match.VectorScore != b.match.VectorScore {
			return a.match.VectorScore > b.match.VectorScore
		}
		return a.match.Record.Key < b.match.Record.Key
	})
	sort.Slice(lexicalOrder, func(i, j int) bool {
		a, b := candidates[lexicalOrder[i]], candidates[lexicalOrder[j]]
		if a.lexical != b.lexical {
			return a.lexical > b.lexical
		}
		return a.match.Record.Key < b.match.Record.Key
	})
	for rank, index := range vectorOrder {
		candidates[index].match.VectorRank = rank + 1
		candidates[index].match.HybridScore += 1 / (rrfConstant + float64(rank+1))
	}
	for rank, index := range lexicalOrder {
		candidates[index].match.LexicalRank = rank + 1
		candidates[index].match.LexicalScore = candidates[index].lexical
		candidates[index].match.HybridScore += 1 / (rrfConstant + float64(rank+1))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].match.HybridScore != candidates[j].match.HybridScore {
			return candidates[i].match.HybridScore > candidates[j].match.HybridScore
		}
		return candidates[i].match.Record.Key < candidates[j].match.Record.Key
	})
	total := len(candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	matches := make([]MemoryMatch, len(candidates))
	for i := range candidates {
		matches[i] = candidates[i].match
	}
	return MemorySearchResponse{Matches: matches, Total: total, Truncated: total > len(matches), Modes: []string{"semantic", "lexical"}, Model: s.config.Model, Dimensions: s.config.Dimensions}, nil
}

func validateMemory(key, kind, content string, confidence float64, provenance []string) (string, string, []string, error) {
	key, kind, content = strings.TrimSpace(key), strings.TrimSpace(kind), strings.TrimSpace(content)
	if !memoryKeyPattern.MatchString(key) {
		return "", "", nil, fail(CodeInvalidQuery, "The memory key must be 1-200 lowercase letters, digits, dots, slashes, underscores, or hyphens.", false, nil)
	}
	if !validMemoryKind(kind) {
		return "", "", nil, fail(CodeInvalidQuery, "Memory kind must be decision, convention, fact, or note.", false, nil)
	}
	if content == "" || len(content) > maximumMemoryContent {
		return "", "", nil, fail(CodeInvalidQuery, "Memory content must contain between 1 and 65536 bytes.", false, nil)
	}
	if confidence < 0 || confidence > 1 {
		return "", "", nil, fail(CodeInvalidQuery, "Memory confidence must be between 0 and 1.", false, nil)
	}
	if len(provenance) > 100 {
		return "", "", nil, fail(CodeInvalidQuery, "Memory provenance cannot exceed 100 paths.", false, nil)
	}
	seen := map[string]bool{}
	normalized := []string{}
	for _, value := range provenance {
		value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
		clean := path.Clean(value)
		if clean == "." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			return "", "", nil, fail(CodeInvalidQuery, "Memory provenance paths must be project-relative.", false, nil)
		}
		if !seen[clean] {
			seen[clean] = true
			normalized = append(normalized, clean)
		}
	}
	sort.Strings(normalized)
	return key, kind, normalized, nil
}

func validMemoryKind(kind string) bool {
	return kind == "decision" || kind == "convention" || kind == "fact" || kind == "note"
}

func memoryEmbeddingText(key, kind, content string, provenance []string) string {
	return "key: " + key + "\nkind: " + kind + "\nprovenance: " + strings.Join(provenance, ", ") + "\n" + strings.TrimSpace(content)
}
