package searchindex

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/sourcegraph/zoekt"
)

// MaxWordFiles bounds how many files one identifier lookup will name.
//
// The consumer parses every file it is given, so this is a work bound and not a
// display bound. A name like `err` appears in most files of a Go project, and the
// honest answer there is a truncated list with the truncation reported rather than
// a minute of parsing.
const MaxWordFiles = 200

// FilesContainingWord returns the project-relative paths whose content contains
// the given identifier as a whole word.
//
// It does not page. Paging is right for showing matches to a caller and wrong for
// this: a name occurring a thousand times in one file would fill a page and hide
// every other file, and the caller here wants the set of files rather than the
// first fifty lines.
//
// The word is validated rather than escaped. An identifier lookup has no business
// accepting a regular expression, and refusing one is clearer than quietly
// matching something the caller did not intend.
func (s *Store) FilesContainingWord(ctx context.Context, word string, maxFiles int) ([]string, bool, error) {
	if err := validIdentifier(word); err != nil {
		return nil, false, fail(CodeInvalidPattern, err.Error(), false, nil)
	}
	if maxFiles <= 0 || maxFiles > MaxWordFiles {
		maxFiles = MaxWordFiles
	}

	dir, state, err := s.resolveCurrent()
	if err != nil {
		return nil, false, err
	}
	// A word boundary on both sides, so `Read` does not match `ReadAll`. The
	// remaining false positives -- the same letters in a comment or a string -- are
	// removed by the consumer, which parses the file.
	q, err := buildQuery(Request{
		Pattern:       `\b` + word + `\b`,
		Mode:          ModeRegex,
		CaseSensitive: true,
	})
	if err != nil {
		return nil, false, fail(CodeInvalidPattern, err.Error(), false, nil)
	}

	searcher, err := s.searcherFor(dir, state.Generation)
	if err != nil {
		return nil, false, err
	}

	s.mu.RLock()
	result, searchErr := searcher.Search(ctx, q, &zoekt.SearchOptions{
		Whole:              false,
		MaxDocDisplayCount: maxDocuments,
	})
	s.mu.RUnlock()
	if searchErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, false, internal("the backend search failed", searchErr)
	}

	seen := make(map[string]struct{}, len(result.Files))
	paths := make([]string, 0, len(result.Files))
	for _, file := range result.Files {
		name := filepath.ToSlash(file.FileName)
		if _, already := seen[name]; already {
			continue
		}
		seen[name] = struct{}{}
		paths = append(paths, name)
	}
	sort.Strings(paths)

	// Truncation is reported rather than implied by a short list, because a caller
	// that reads a truncated answer as complete draws exactly the wrong conclusion
	// from "no other references".
	truncated := len(result.Files) >= maxDocuments
	if len(paths) > maxFiles {
		paths = paths[:maxFiles]
		truncated = true
	}
	return paths, truncated, nil
}

// validIdentifier rejects anything that is not a plausible identifier.
//
// The check is deliberately generous about what a language may allow -- letters,
// digits, underscore, and dollar -- and strict about what a regular expression
// needs, which is the point: nothing that reaches the query can change what it
// matches.
func validIdentifier(word string) error {
	if word == "" {
		return fmt.Errorf("the name is empty")
	}
	if len(word) > 256 {
		return fmt.Errorf("the name is longer than any identifier")
	}
	for index, r := range word {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == '$':
		case r >= '0' && r <= '9' && index > 0:
		case r > 127:
			// A non-ASCII letter is a legal identifier in every language here, and it
			// carries no regular-expression meaning.
		default:
			return fmt.Errorf("%q is not an identifier; a name may hold letters, digits, underscores, and dollars", word)
		}
	}
	return nil
}
