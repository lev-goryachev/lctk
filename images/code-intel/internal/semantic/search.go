package semantic

import (
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

// Search performs exact sqlite-vec KNN and a deterministic lexical ranking,
// then fuses independent ranks with reciprocal-rank fusion. The lexical pass is
// intentionally LCTK-owned instead of depending on SQLite build tags; it keeps
// identifiers competitive with prose without making FTS availability part of
// the container contract.
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
	var vectorCandidates []rankedChunk
	for vectorRows.Next() {
		var row rankedChunk
		var encoded []byte
		if err := vectorRows.Scan(&row.id, &row.match.Path, &row.match.Language,
			&row.match.ChunkPrecision, &row.match.Anchor, &row.match.StartLine,
			&row.match.EndLine, &row.match.Preview, &encoded); err != nil {
			vectorRows.Close()
			return Response{}, fail(CodeCorrupt, "A semantic vector result is invalid.", false, err)
		}
		stored, err := deserializeVector(encoded, s.config.Dimensions)
		if err != nil {
			vectorRows.Close()
			return Response{}, err
		}
		row.match.VectorScore = cosine(vectors[0], stored)
		vectorCandidates = append(vectorCandidates, row)
	}
	if err := vectorRows.Close(); err != nil {
		return Response{}, fail(CodeCorrupt, "The semantic vector search did not complete.", false, err)
	}
	sort.Slice(vectorCandidates, func(i, j int) bool {
		if vectorCandidates[i].match.VectorScore != vectorCandidates[j].match.VectorScore {
			return vectorCandidates[i].match.VectorScore > vectorCandidates[j].match.VectorScore
		}
		if vectorCandidates[i].match.Path != vectorCandidates[j].match.Path {
			return vectorCandidates[i].match.Path < vectorCandidates[j].match.Path
		}
		return vectorCandidates[i].match.StartLine < vectorCandidates[j].match.StartLine
	})
	if len(vectorCandidates) > vectorLimit {
		vectorCandidates = vectorCandidates[:vectorLimit]
	}
	combined := make(map[int64]*rankedChunk)
	for rank := range vectorCandidates {
		row := vectorCandidates[rank]
		row.match.VectorRank = rank + 1
		row.match.HybridScore = 1 / (rrfConstant + float64(rank+1))
		row.match.Preview = boundedPreview(row.match.Preview)
		combined[row.id] = &row
	}

	terms := queryTerms(query)
	lexical, err := s.lexicalCandidates(ctx, terms)
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
		return all[i].StartLine < all[j].StartLine
	})
	total := len(all)
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

func (s *Store) lexicalCandidates(ctx context.Context, terms []string) ([]rankedChunk, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, path, language, precision, anchor,
start_line, end_line, content FROM semantic_chunks`)
	if err != nil {
		return nil, fail(CodeCorrupt, "Semantic chunks could not be read for lexical ranking.", false, err)
	}
	defer rows.Close()
	var results []rankedChunk
	for rows.Next() {
		var row rankedChunk
		if err := rows.Scan(&row.id, &row.match.Path, &row.match.Language,
			&row.match.ChunkPrecision, &row.match.Anchor, &row.match.StartLine,
			&row.match.EndLine, &row.match.Preview); err != nil {
			return nil, fail(CodeCorrupt, "A semantic chunk is invalid.", false, err)
		}
		haystack := strings.ToLower(row.match.Path + "\n" + row.match.Anchor + "\n" + row.match.Preview)
		for _, term := range terms {
			count := strings.Count(haystack, term)
			if count > 0 {
				row.lexicalRaw += float64(count)
			}
		}
		if row.lexicalRaw > 0 {
			results = append(results, row)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fail(CodeCorrupt, "Lexical semantic ranking did not complete.", false, err)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].lexicalRaw != results[j].lexicalRaw {
			return results[i].lexicalRaw > results[j].lexicalRaw
		}
		if results[i].match.Path != results[j].match.Path {
			return results[i].match.Path < results[j].match.Path
		}
		return results[i].match.StartLine < results[j].match.StartLine
	})
	return results, nil
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
