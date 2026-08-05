package semantic

import (
	"container/heap"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	defaultLimit = 20
	maximumLimit = 100
	rrfConstant  = 60.0
)

// Request is one bounded semantic retrieval request.
type Request struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// Match is one structural or text chunk with transparent hybrid evidence.
type Match struct {
	Path           string  `json:"path"`
	Language       string  `json:"language"`
	ChunkPrecision string  `json:"chunk_precision"`
	Anchor         string  `json:"anchor"`
	StartLine      int     `json:"start_line"`
	EndLine        int     `json:"end_line"`
	Preview        string  `json:"preview"`
	VectorScore    float64 `json:"vector_score,omitempty"`
	LexicalScore   float64 `json:"lexical_score,omitempty"`
	HybridScore    float64 `json:"hybrid_score"`
	VectorRank     int     `json:"vector_rank,omitempty"`
	LexicalRank    int     `json:"lexical_rank,omitempty"`
}

// Response carries the semantic publication identity needed to judge an answer.
type Response struct {
	Matches         []Match `json:"matches"`
	Total           int     `json:"total"`
	Truncated       bool    `json:"truncated"`
	Generation      uint64  `json:"generation"`
	ExactGeneration uint64  `json:"exact_generation"`
	Freshness       string  `json:"freshness"`
	Model           string  `json:"model"`
	Dimensions      int     `json:"dimensions"`
}

type rankedChunk struct {
	id         int64
	match      Match
	lexicalRaw float64
}

type candidateHeap struct {
	items  []rankedChunk
	better func(rankedChunk, rankedChunk) bool
}

func (values candidateHeap) Len() int { return len(values.items) }
func (values candidateHeap) Less(left, right int) bool {
	// The worst retained candidate is the root, so a better new candidate can
	// replace it in O(log K) while every corpus row is still scored exactly.
	return values.better(values.items[right], values.items[left])
}
func (values candidateHeap) Swap(left, right int) {
	values.items[left], values.items[right] = values.items[right], values.items[left]
}
func (values *candidateHeap) Push(value any) {
	values.items = append(values.items, value.(rankedChunk))
}
func (values *candidateHeap) Pop() any {
	last := len(values.items) - 1
	value := values.items[last]
	values.items = values.items[:last]
	return value
}

func retainCandidate(values *candidateHeap, candidate rankedChunk, limit int) {
	if values.Len() < limit {
		heap.Push(values, candidate)
		return
	}
	if values.better(candidate, values.items[0]) {
		values.items[0] = candidate
		heap.Fix(values, 0)
	}
}

// Search performs an exact owned cosine scan and a deterministic lexical
// ranking, then fuses independent ranks with reciprocal-rank fusion. Both scans
// retain only their bounded best candidates while counting the full lexical
// result union, so corpus size does not become response-memory size. The
// lexical pass is intentionally LCTK-owned instead of depending on SQLite
// build tags; it keeps identifiers competitive with prose without making FTS
// availability part of the container contract.
func (s *Store) Search(ctx context.Context, request Request) (Response, error) {
	query := strings.TrimSpace(request.Query)
	if query == "" {
		return Response{}, fail(CodeInvalidQuery, "The semantic query is empty.", false, nil)
	}
	if len(query) > 8192 {
		return Response{}, fail(CodeInvalidQuery, "The semantic query exceeds 8192 bytes.", false, nil)
	}
	limit := request.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > maximumLimit {
		return Response{}, fail(CodeInvalidQuery, "The semantic result limit must be between 1 and 100.", false, nil)
	}
	status, err := s.Status()
	if err != nil {
		return Response{}, err
	}
	if !status.Ready {
		return Response{}, fail(CodeNotReady, status.Reason, true, nil)
	}
	vectors, err := s.embedder.Embed(ctx, EmbeddingQuery, []string{query})
	if err != nil {
		return Response{}, err
	}
	vectorLimit := limit * 4
	if vectorLimit < 50 {
		vectorLimit = 50
	}
	vectorRows, err := s.db.QueryContext(ctx, `SELECT c.id, c.path, c.language, c.precision,
c.anchor, c.start_line, c.end_line, c.content, v.embedding
FROM semantic_vectors v JOIN semantic_chunks c ON c.id = v.rowid`)
	if err != nil {
		return Response{}, fail(CodeCorrupt, "The semantic vector index could not be searched.", false, err)
	}
	vectorCandidates := &candidateHeap{better: vectorBetter}
	heap.Init(vectorCandidates)
	for vectorRows.Next() {
		var row rankedChunk
		var encoded []byte
		if err := vectorRows.Scan(&row.id, &row.match.Path, &row.match.Language,
			&row.match.ChunkPrecision, &row.match.Anchor, &row.match.StartLine,
			&row.match.EndLine, &row.match.Preview, &encoded); err != nil {
			vectorRows.Close()
			return Response{}, fail(CodeCorrupt, "A semantic vector result is invalid.", false, err)
		}
		score, err := cosineEncoded(vectors[0], encoded, s.config.Dimensions)
		if err != nil {
			vectorRows.Close()
			return Response{}, err
		}
		row.match.VectorScore = score
		retainCandidate(vectorCandidates, row, vectorLimit)
	}
	if err := vectorRows.Close(); err != nil {
		return Response{}, fail(CodeCorrupt, "The semantic vector search did not complete.", false, err)
	}
	sort.Slice(vectorCandidates.items, func(i, j int) bool {
		return vectorBetter(vectorCandidates.items[i], vectorCandidates.items[j])
	})
	combined := make(map[int64]*rankedChunk)
	vectorIDs := make(map[int64]bool, len(vectorCandidates.items))
	for rank := range vectorCandidates.items {
		row := vectorCandidates.items[rank]
		row.match.VectorRank = rank + 1
		row.match.HybridScore = 1 / (rrfConstant + float64(rank+1))
		row.match.Preview = boundedPreview(row.match.Preview)
		combined[row.id] = &row
		vectorIDs[row.id] = true
	}

	terms := queryTerms(query)
	lexical, lexicalTotal, vectorLexicalMatches, err := s.lexicalCandidates(ctx, terms, vectorLimit, vectorIDs)
	if err != nil {
		return Response{}, err
	}
	for rank, candidate := range lexical {
		position := rank + 1
		current, exists := combined[candidate.id]
		if !exists {
			copy := candidate
			copy.match.Preview = boundedPreview(copy.match.Preview)
			current = &copy
			combined[candidate.id] = current
		}
		current.match.LexicalRank = position
		current.match.LexicalScore = candidate.lexicalRaw
		current.match.HybridScore += 1 / (rrfConstant + float64(position))
	}

	all := make([]Match, 0, len(combined))
	for _, candidate := range combined {
		all = append(all, candidate.match)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].HybridScore != all[j].HybridScore {
			return all[i].HybridScore > all[j].HybridScore
		}
		if all[i].Path != all[j].Path {
			return all[i].Path < all[j].Path
		}
		if all[i].StartLine != all[j].StartLine {
			return all[i].StartLine < all[j].StartLine
		}
		if all[i].EndLine != all[j].EndLine {
			return all[i].EndLine < all[j].EndLine
		}
		return all[i].Anchor < all[j].Anchor
	})
	// The ranked working set is bounded, but Total remains the exact union of all
	// lexical matches and the vector candidates that did not match lexically.
	total := lexicalTotal + len(vectorCandidates.items) - vectorLexicalMatches
	if len(all) > limit {
		all = all[:limit]
	}
	return Response{
		Matches: all, Total: total, Truncated: total > len(all), Generation: status.Generation,
		Model: s.config.Model, Dimensions: s.config.Dimensions,
	}, nil
}

func deserializeVector(encoded []byte, dimensions int) ([]float32, error) {
	if len(encoded) != dimensions*4 {
		return nil, fail(CodeCorrupt,
			fmt.Sprintf("A stored vector has %d bytes; %d are required.", len(encoded), dimensions*4), false, nil)
	}
	vector := make([]float32, dimensions)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[i*4:]))
	}
	return vector, nil
}

func cosine(left, right []float32) float64 {
	var value float64
	for i := range left {
		value += float64(left[i]) * float64(right[i])
	}
	return value
}

func cosineEncoded(query []float32, encoded []byte, dimensions int) (float64, error) {
	if len(query) != dimensions || len(encoded) != dimensions*4 {
		return 0, fail(CodeCorrupt,
			fmt.Sprintf("A stored vector has %d bytes; %d are required.", len(encoded), dimensions*4), false, nil)
	}
	var value float64
	for index, queryValue := range query {
		stored := math.Float32frombits(binary.LittleEndian.Uint32(encoded[index*4:]))
		value += float64(queryValue) * float64(stored)
	}
	return value, nil
}

func vectorBetter(left, right rankedChunk) bool {
	if left.match.VectorScore != right.match.VectorScore {
		return left.match.VectorScore > right.match.VectorScore
	}
	if left.match.Path != right.match.Path {
		return left.match.Path < right.match.Path
	}
	if left.match.StartLine != right.match.StartLine {
		return left.match.StartLine < right.match.StartLine
	}
	if left.match.EndLine != right.match.EndLine {
		return left.match.EndLine < right.match.EndLine
	}
	if left.match.Anchor != right.match.Anchor {
		return left.match.Anchor < right.match.Anchor
	}
	return left.id < right.id
}

func (s *Store) lexicalCandidates(ctx context.Context, terms []string, limit int,
	vectorIDs map[int64]bool) ([]rankedChunk, int, int, error) {
	if len(terms) == 0 {
		return nil, 0, 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, path, language, precision, anchor,
start_line, end_line, content FROM semantic_chunks`)
	if err != nil {
		return nil, 0, 0, fail(CodeCorrupt, "Semantic chunks could not be read for lexical ranking.", false, err)
	}
	defer rows.Close()
	results := &candidateHeap{better: lexicalBetter}
	heap.Init(results)
	lexicalTotal := 0
	vectorLexicalMatches := 0
	for rows.Next() {
		var row rankedChunk
		if err := rows.Scan(&row.id, &row.match.Path, &row.match.Language,
			&row.match.ChunkPrecision, &row.match.Anchor, &row.match.StartLine,
			&row.match.EndLine, &row.match.Preview); err != nil {
			return nil, 0, 0, fail(CodeCorrupt, "A semantic chunk is invalid.", false, err)
		}
		haystack := strings.ToLower(row.match.Path + "\n" + row.match.Anchor + "\n" + row.match.Preview)
		for _, term := range terms {
			count := strings.Count(haystack, term)
			if count > 0 {
				row.lexicalRaw += float64(count)
			}
		}
		if row.lexicalRaw > 0 {
			lexicalTotal++
			if vectorIDs[row.id] {
				vectorLexicalMatches++
			}
			retainCandidate(results, row, limit)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, fail(CodeCorrupt, "Lexical semantic ranking did not complete.", false, err)
	}
	sort.Slice(results.items, func(i, j int) bool {
		return lexicalBetter(results.items[i], results.items[j])
	})
	return results.items, lexicalTotal, vectorLexicalMatches, nil
}

func lexicalBetter(left, right rankedChunk) bool {
	if left.lexicalRaw != right.lexicalRaw {
		return left.lexicalRaw > right.lexicalRaw
	}
	if left.match.Path != right.match.Path {
		return left.match.Path < right.match.Path
	}
	if left.match.StartLine != right.match.StartLine {
		return left.match.StartLine < right.match.StartLine
	}
	if left.match.EndLine != right.match.EndLine {
		return left.match.EndLine < right.match.EndLine
	}
	if left.match.Anchor != right.match.Anchor {
		return left.match.Anchor < right.match.Anchor
	}
	return left.id < right.id
}

func queryTerms(query string) []string {
	seen := make(map[string]bool)
	var terms []string
	for _, field := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	}) {
		if len(field) < 2 || seen[field] {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
	}
	return terms
}

func boundedPreview(value string) string {
	const maximum = 1200
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return strings.TrimSpace(value[:maximum]) + "…"
}
