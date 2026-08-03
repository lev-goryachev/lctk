package searchindex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/search"
)

const (
	// DefaultLimit is the page size when the caller does not choose one. It is
	// small on purpose: the consumer is an agent with a context window, and a
	// hundred half-relevant lines cost more than they inform.
	DefaultLimit = 50
	// MaxLimit is the largest page a caller may request.
	MaxLimit = 500
	// maxPreviewBytes bounds a single returned line.
	maxPreviewBytes = 512
	// maxDocuments bounds how many files the backend will consider for one query,
	// so a pattern matching everything cannot turn into an unbounded scan.
	maxDocuments = 10000
)

// Mode selects how the pattern is interpreted.
const (
	ModeLiteral = "literal"
	ModeRegex   = "regex"
)

// Request is LCTK's search request. It names no Zoekt concept.
type Request struct {
	Pattern       string   `json:"pattern"`
	Mode          string   `json:"mode,omitempty"`
	CaseSensitive bool     `json:"case_sensitive,omitempty"`
	PathGlobs     []string `json:"path_globs,omitempty"`
	Languages     []string `json:"languages,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	Cursor        string   `json:"cursor,omitempty"`
}

// Match is one matching line fragment, with a project-relative path.
type Match struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Preview string `json:"preview"`
	Match   string `json:"match"`
}

// Response carries the page and the provenance a caller needs to trust it.
type Response struct {
	Matches []Match `json:"matches"`
	// Total is the number of matches in the whole result set, not the page, so a
	// caller can tell "five results" from "the first five of nine hundred".
	Total      int    `json:"total"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor,omitempty"`
	// Generation is the index generation that answered. Two responses with the
	// same generation describe the same snapshot of the project.
	Generation uint64 `json:"generation"`
	IndexedAt  string `json:"indexed_at"`
	FileCount  int    `json:"file_count"`
}

type cursor struct {
	Generation uint64 `json:"g"`
	Offset     int    `json:"o"`
}

// Search executes one query against the published generation.
func (s *Store) Search(ctx context.Context, request Request) (Response, error) {
	dir, state, err := s.resolveCurrent()
	if err != nil {
		return Response{}, err
	}

	if strings.TrimSpace(request.Pattern) == "" {
		return Response{}, fail(CodeInvalidPattern, "The search pattern is empty.", false, nil)
	}
	switch {
	case request.Limit < 0:
		return Response{}, fail(CodeInvalidPattern, "The result limit is negative.", false, nil)
	case request.Limit == 0:
		request.Limit = DefaultLimit
	case request.Limit > MaxLimit:
		return Response{}, fail(CodeLimitExceeded,
			fmt.Sprintf("The result limit %d exceeds the maximum of %d.", request.Limit, MaxLimit), false, nil)
	}

	q, err := buildQuery(request)
	if err != nil {
		return Response{}, fail(CodeInvalidPattern, err.Error(), false, nil)
	}

	offset, err := decodeCursor(request.Cursor, state.Generation)
	if err != nil {
		return Response{}, fail(CodeInvalidCursor, err.Error(), false, nil)
	}

	searcher, err := s.searcherFor(dir, state.Generation)
	if err != nil {
		return Response{}, err
	}

	s.mu.RLock()
	result, searchErr := searcher.Search(ctx, q, &zoekt.SearchOptions{
		Whole:              true,
		MaxDocDisplayCount: maxDocuments,
	})
	s.mu.RUnlock()
	if searchErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, ctxErr
		}
		return Response{}, internal("the backend search failed", searchErr)
	}

	matches := flatten(result)
	sort.Slice(matches, func(a, b int) bool {
		switch {
		case matches[a].Path != matches[b].Path:
			return matches[a].Path < matches[b].Path
		case matches[a].Line != matches[b].Line:
			return matches[a].Line < matches[b].Line
		default:
			return matches[a].Column < matches[b].Column
		}
	})

	if offset > len(matches) {
		return Response{}, fail(CodeInvalidCursor,
			"The cursor points beyond the end of the result set.", false, nil)
	}
	end := min(offset+request.Limit, len(matches))
	page := matches[offset:end]

	response := Response{
		Matches:    page,
		Total:      len(matches),
		Truncated:  result.Files != nil && len(result.Files) >= maxDocuments,
		Generation: state.Generation,
		IndexedAt:  state.BuiltAt.Format("2006-01-02T15:04:05Z"),
		FileCount:  state.FileCount,
	}
	if response.Matches == nil {
		response.Matches = []Match{}
	}
	if end < len(matches) {
		encoded, err := encodeCursor(cursor{Generation: state.Generation, Offset: end})
		if err != nil {
			return Response{}, internal("encode the pagination cursor", err)
		}
		response.NextCursor = encoded
	}
	return response, nil
}

// searcherFor returns a searcher for the published generation, reusing the open
// one when the generation has not changed.
//
// A long-lived service should not reopen every shard per query, and the cached
// searcher is swapped only on publication, so a query never observes a mixture
// of two generations.
func (s *Store) searcherFor(dir string, generation uint64) (zoekt.Searcher, error) {
	s.mu.RLock()
	if s.searcher != nil && s.openGen == generation {
		defer s.mu.RUnlock()
		return s.searcher, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searcher != nil && s.openGen == generation {
		return s.searcher, nil
	}
	opened, err := search.NewDirectorySearcher(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fail(CodeIndexNotReady, "The project index has not been built yet.", true, err)
		}
		return nil, fail(CodeIndexCorrupt, "The persistent index cannot be opened.", false, err)
	}
	if s.searcher != nil {
		s.searcher.Close()
	}
	s.searcher = opened
	s.openGen = generation
	return opened, nil
}

// invalidate drops the cached searcher so the next query opens the new
// generation.
func (s *Store) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searcher != nil {
		s.searcher.Close()
		s.searcher = nil
	}
	s.openGen = 0
}

// Close releases the cached searcher.
func (s *Store) Close() {
	s.invalidate()
}

func flatten(result *zoekt.SearchResult) []Match {
	var matches []Match
	for _, file := range result.Files {
		name := filepath.ToSlash(file.FileName)
		for _, line := range file.LineMatches {
			text := strings.TrimSuffix(string(line.Line), "\n")
			for _, fragment := range line.LineFragments {
				start := fragment.LineOffset
				end := min(start+fragment.MatchLength, len(line.Line))
				if start < 0 || start > end {
					continue
				}
				matches = append(matches, Match{
					Path:    name,
					Line:    line.LineNumber,
					Column:  start + 1,
					Preview: boundedPreview(text, start, end),
					Match:   string(line.Line[start:end]),
				})
			}
		}
	}
	return matches
}

func encodeCursor(value cursor) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

// decodeCursor rejects a cursor from another generation.
//
// Paging through a result set that was computed against a different index would
// silently skip or repeat results. Refusing is the honest answer: the caller
// re-runs the query and learns the index moved.
func decodeCursor(value string, generation uint64) (int, error) {
	if value == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, fmt.Errorf("the cursor is not a valid token")
	}
	var parsed cursor
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, fmt.Errorf("the cursor is not a valid token")
	}
	if parsed.Generation != generation {
		// States what happened and stops there. What to do about it is the caller's
		// recommended action, which is a separate field precisely so the two are not
		// welded into one sentence.
		return 0, fmt.Errorf("the cursor belongs to index generation %d and the current generation is %d",
			parsed.Generation, generation)
	}
	if parsed.Offset < 0 {
		return 0, fmt.Errorf("the cursor offset is negative")
	}
	return parsed.Offset, nil
}
