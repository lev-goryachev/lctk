// Package zoektadapter is an evidence-only working-tree adapter used by the
// Slice 0.3 search-backend evaluation. It is not production LCTK code.
package zoektadapter

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp/syntax"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
	"github.com/sourcegraph/zoekt/search"
)

const (
	manifestName    = "lctk-manifest.json"
	maxLimit        = 1000
	maxPreviewBytes = 512
)

var defaultExcludedDirectories = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {}, "node_modules": {},
	"dist": {}, "build": {}, "coverage": {}, ".venv": {}, "vendor": {}, "generated": {},
}

type Error struct {
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Code + ": " + e.Message
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

func adapterError(code, message string, retryable bool, cause error) error {
	return &Error{Code: code, Message: message, Retryable: retryable, Cause: cause}
}

type Manifest struct {
	Generation uint64            `json:"generation"`
	Files      map[string]string `json:"files"`
}

type Change struct {
	Path    string
	Deleted bool
}

type Indexer struct {
	Workspace string
	IndexDir  string
	ProjectID string
}

type Request struct {
	Pattern       string
	Mode          string
	CaseSensitive bool
	PathGlobs     []string
	Languages     []string
	Limit         int
	Cursor        string
}

type Match struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Preview string `json:"preview"`
	Match   string `json:"match"`
}

type Response struct {
	Matches    []Match `json:"matches"`
	NextCursor string  `json:"next_cursor,omitempty"`
	Generation uint64  `json:"generation"`
}

type Session struct {
	indexer  Indexer
	searcher zoekt.Streamer
}

type cursor struct {
	Generation uint64 `json:"generation"`
	Offset     int    `json:"offset"`
}

func (i Indexer) Full(ctx context.Context) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(i.IndexDir, 0o755); err != nil {
		return Manifest{}, err
	}
	previous, err := i.loadManifest()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	files, err := i.inventory(ctx)
	if err != nil {
		return Manifest{}, err
	}
	if err := i.removeShards(); err != nil {
		return Manifest{}, err
	}
	builder, err := index.NewBuilder(i.options(false))
	if err != nil {
		return Manifest{}, err
	}
	for _, name := range sortedKeys(files) {
		content, readErr := os.ReadFile(filepath.Join(i.Workspace, filepath.FromSlash(name)))
		if readErr != nil {
			return Manifest{}, readErr
		}
		if err := builder.AddFile(name, content); err != nil {
			return Manifest{}, err
		}
	}
	if err := builder.Finish(); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{Generation: previous.Generation + 1, Files: files}
	if err := i.storeManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (i Indexer) Apply(ctx context.Context, changes []Change) (Manifest, error) {
	manifest, err := i.loadManifest()
	if err != nil {
		return Manifest{}, adapterError("INDEX_CORRUPT", "The index manifest cannot be read.", false, err)
	}
	if len(changes) == 0 {
		return manifest, nil
	}
	builder, err := index.NewBuilder(i.options(true))
	if err != nil {
		return Manifest{}, err
	}
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		name, err := normalizeRelative(change.Path)
		if err != nil {
			return Manifest{}, err
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		builder.MarkFileAsChangedOrRemoved(name)
		absolute := filepath.Join(i.Workspace, filepath.FromSlash(name))
		content, readErr := os.ReadFile(absolute)
		if change.Deleted || errors.Is(readErr, os.ErrNotExist) {
			delete(manifest.Files, name)
			continue
		}
		if readErr != nil {
			return Manifest{}, readErr
		}
		if err := builder.AddFile(name, content); err != nil {
			return Manifest{}, err
		}
		manifest.Files[name] = digest(content)
	}
	if err := builder.Finish(); err != nil {
		return Manifest{}, err
	}
	manifest.Generation++
	if err := i.storeManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (i Indexer) Reconcile(ctx context.Context) (Manifest, []Change, error) {
	manifest, err := i.loadManifest()
	if err != nil {
		return Manifest{}, nil, err
	}
	current, err := i.inventory(ctx)
	if err != nil {
		return Manifest{}, nil, err
	}
	var changes []Change
	for name, hash := range current {
		if manifest.Files[name] != hash {
			changes = append(changes, Change{Path: name})
		}
	}
	for name := range manifest.Files {
		if _, exists := current[name]; !exists {
			changes = append(changes, Change{Path: name, Deleted: true})
		}
	}
	sort.Slice(changes, func(a, b int) bool { return changes[a].Path < changes[b].Path })
	updated, err := i.Apply(ctx, changes)
	return updated, changes, err
}

func (i Indexer) Search(ctx context.Context, request Request) (Response, error) {
	searcher, err := search.NewDirectorySearcher(i.IndexDir)
	if err != nil {
		return Response{}, classifyOpenError(err)
	}
	defer searcher.Close()
	return i.searchWith(ctx, searcher, request)
}

func (i Indexer) OpenSession() (*Session, error) {
	searcher, err := search.NewDirectorySearcher(i.IndexDir)
	if err != nil {
		return nil, classifyOpenError(err)
	}
	return &Session{indexer: i, searcher: searcher}, nil
}

func (s *Session) Close() {
	s.searcher.Close()
}

func (s *Session) Search(ctx context.Context, request Request) (Response, error) {
	return s.indexer.searchWith(ctx, s.searcher, request)
}

func (i Indexer) searchWith(ctx context.Context, searcher zoekt.Searcher, request Request) (Response, error) {
	manifest, err := i.loadManifest()
	if err != nil {
		return Response{}, adapterError("INDEX_CORRUPT", "The index manifest cannot be read.", false, err)
	}
	if request.Pattern == "" {
		return Response{}, adapterError("INVALID_PATTERN", "The search pattern is empty.", false, nil)
	}
	if request.Limit <= 0 {
		request.Limit = 50
	}
	if request.Limit > maxLimit {
		return Response{}, adapterError("LIMIT_EXCEEDED", "The result limit exceeds the maximum.", false, nil)
	}
	q, err := buildQuery(request)
	if err != nil {
		return Response{}, adapterError("INVALID_PATTERN", "The search pattern or path glob is invalid.", false, err)
	}
	result, err := searcher.Search(ctx, q, &zoekt.SearchOptions{Whole: true, MaxDocDisplayCount: 10000})
	if err != nil {
		return Response{}, adapterError("INTERNAL_ERROR", "The backend search failed.", false, err)
	}
	matches := flatten(result, request.PathGlobs)
	sort.Slice(matches, func(a, b int) bool {
		if matches[a].Path != matches[b].Path {
			return matches[a].Path < matches[b].Path
		}
		if matches[a].Line != matches[b].Line {
			return matches[a].Line < matches[b].Line
		}
		return matches[a].Column < matches[b].Column
	})
	offset, err := decodeCursor(request.Cursor, manifest.Generation)
	if err != nil {
		return Response{}, adapterError("INVALID_CURSOR", "The search cursor is invalid or stale.", false, err)
	}
	if offset > len(matches) {
		return Response{}, adapterError("INVALID_CURSOR", "The search cursor is beyond the result set.", false, nil)
	}
	end := min(offset+request.Limit, len(matches))
	response := Response{Matches: matches[offset:end], Generation: manifest.Generation}
	if end < len(matches) {
		response.NextCursor, err = encodeCursor(cursor{Generation: manifest.Generation, Offset: end})
	}
	return response, err
}

func (i Indexer) options(delta bool) index.Options {
	opts := index.Options{
		IndexDir: i.IndexDir,
		RepositoryDescription: zoekt.Repository{
			ID: 1, Name: i.ProjectID,
		},
		DisableCTags: true,
		IsDelta:      delta,
	}
	opts.SetDefaults()
	return opts
}

func (i Indexer) inventory(ctx context.Context) (map[string]string, error) {
	files := make(map[string]string)
	err := filepath.WalkDir(i.Workspace, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if name != i.Workspace {
				if _, excluded := defaultExcludedDirectories[entry.Name()]; excluded {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(i.Workspace, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		files[relative] = digest(content)
		return nil
	})
	return files, err
}

func (i Indexer) loadManifest() (Manifest, error) {
	content, err := os.ReadFile(filepath.Join(i.IndexDir, manifestName))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.Files == nil {
		manifest.Files = make(map[string]string)
	}
	return manifest, nil
}

func (i Indexer) storeManifest(manifest Manifest) error {
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temporary := filepath.Join(i.IndexDir, manifestName+".tmp")
	if err := os.WriteFile(temporary, append(content, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(i.IndexDir, manifestName))
}

func (i Indexer) removeShards() error {
	matches, err := filepath.Glob(filepath.Join(i.IndexDir, "*.zoekt*"))
	if err != nil {
		return err
	}
	for _, name := range matches {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func buildQuery(request Request) (query.Q, error) {
	var content query.Q
	switch request.Mode {
	case "", "literal":
		content = &query.Substring{Pattern: request.Pattern, CaseSensitive: request.CaseSensitive, Content: true}
	case "regex":
		parsed, err := syntax.Parse(request.Pattern, syntax.ClassNL|syntax.PerlX|syntax.UnicodeGroups)
		if err != nil {
			return nil, err
		}
		content = &query.Regexp{Regexp: parsed, CaseSensitive: request.CaseSensitive, Content: true}
	default:
		return nil, fmt.Errorf("unsupported mode %q", request.Mode)
	}
	parts := []query.Q{content}
	if len(request.PathGlobs) > 0 {
		paths := make([]query.Q, 0, len(request.PathGlobs))
		for _, glob := range request.PathGlobs {
			expression, err := globRegexp(glob)
			if err != nil {
				return nil, err
			}
			parsed, err := syntax.Parse(expression, syntax.ClassNL|syntax.PerlX|syntax.UnicodeGroups)
			if err != nil {
				return nil, err
			}
			paths = append(paths, &query.Regexp{Regexp: parsed, CaseSensitive: true, FileName: true})
		}
		parts = append(parts, query.NewOr(paths...))
	}
	if len(request.Languages) > 0 {
		languages := make([]query.Q, 0, len(request.Languages))
		for _, language := range request.Languages {
			languages = append(languages, &query.Language{Language: canonicalLanguage(language)})
		}
		parts = append(parts, query.NewOr(languages...))
	}
	return query.NewAnd(parts...), nil
}

func flatten(result *zoekt.SearchResult, _ []string) []Match {
	var matches []Match
	for _, file := range result.Files {
		name := filepath.ToSlash(file.FileName)
		for _, line := range file.LineMatches {
			for _, fragment := range line.LineFragments {
				start := fragment.LineOffset
				end := min(start+fragment.MatchLength, len(line.Line))
				if start < 0 || start > end {
					continue
				}
				matches = append(matches, Match{
					Path: name, Line: line.LineNumber, Column: start + 1,
					Preview: boundedPreview(strings.TrimSuffix(string(line.Line), "\n"), start, end),
					Match:   string(line.Line[start:end]),
				})
			}
		}
	}
	return matches
}

func classifyOpenError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return adapterError("INDEX_NOT_READY", "The index has not been built.", true, err)
	}
	return adapterError("INDEX_CORRUPT", "The persistent index cannot be opened.", false, err)
}

func globRegexp(glob string) (string, error) {
	glob = filepath.ToSlash(strings.TrimSpace(glob))
	if glob == "" || strings.HasPrefix(glob, "/") || strings.HasPrefix(glob, "../") {
		return "", fmt.Errorf("glob must be project-relative: %q", glob)
	}
	var expression strings.Builder
	expression.WriteByte('^')
	for offset := 0; offset < len(glob); {
		switch glob[offset] {
		case '*':
			if offset+1 < len(glob) && glob[offset+1] == '*' {
				offset += 2
				if offset < len(glob) && glob[offset] == '/' {
					expression.WriteString("(?:.*/)?")
					offset++
				} else {
					expression.WriteString(".*")
				}
			} else {
				expression.WriteString("[^/]*")
				offset++
			}
		case '?':
			expression.WriteString("[^/]")
			offset++
		case '[':
			end := strings.IndexByte(glob[offset+1:], ']')
			if end < 0 {
				return "", fmt.Errorf("unterminated character class in %q", glob)
			}
			end += offset + 1
			class := glob[offset+1 : end]
			if class == "" {
				return "", fmt.Errorf("empty character class in %q", glob)
			}
			if class[0] == '!' {
				class = "^" + class[1:]
			}
			expression.WriteByte('[')
			expression.WriteString(class)
			expression.WriteByte(']')
			offset = end + 1
		default:
			expression.WriteString(regexpQuoteByte(glob[offset]))
			offset++
		}
	}
	expression.WriteByte('$')
	return expression.String(), nil
}

func regexpQuoteByte(value byte) string {
	if strings.ContainsRune(`\.+()|{}^$`, rune(value)) {
		return `\` + string(value)
	}
	return string(value)
}

func canonicalLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "c++", "cpp", "cxx":
		return "C++"
	case "c#", "csharp":
		return "C#"
	case "css":
		return "CSS"
	case "go", "golang":
		return "Go"
	case "html":
		return "HTML"
	case "javascript", "js":
		return "JavaScript"
	case "json":
		return "JSON"
	case "jsx":
		return "JSX"
	case "markdown", "md":
		return "Markdown"
	case "python", "py":
		return "Python"
	case "rust", "rs":
		return "Rust"
	case "typescript", "ts":
		return "TypeScript"
	case "tsx":
		return "TSX"
	default:
		return language
	}
}

func boundedPreview(line string, matchStart, matchEnd int) string {
	if len(line) <= maxPreviewBytes {
		return line
	}
	start := max(0, matchStart-maxPreviewBytes/2)
	end := min(len(line), max(start+maxPreviewBytes, matchEnd))
	start = max(0, end-maxPreviewBytes)
	for start < matchStart && start < len(line) && !utf8.RuneStart(line[start]) {
		start++
	}
	for end > matchEnd && end > 0 && end < len(line) && !utf8.RuneStart(line[end]) {
		end--
	}
	return line[start:end]
}

func normalizeRelative(name string) (string, error) {
	name = filepath.ToSlash(filepath.Clean(name))
	if name == "." || name == "" || strings.HasPrefix(name, "../") || path.IsAbs(name) {
		return "", fmt.Errorf("path must be project-relative: %q", name)
	}
	return name, nil
}

func digest(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func encodeCursor(value cursor) (string, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(content), nil
}

func decodeCursor(value string, generation uint64) (int, error) {
	if value == "" {
		return 0, nil
	}
	content, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, err
	}
	var parsed cursor
	if err := json.Unmarshal(content, &parsed); err != nil {
		return 0, err
	}
	if parsed.Generation != generation {
		return 0, fmt.Errorf("cursor generation %d does not match index generation %d", parsed.Generation, generation)
	}
	if parsed.Offset < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	return parsed.Offset, nil
}
