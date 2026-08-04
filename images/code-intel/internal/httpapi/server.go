// Package httpapi serves the project's code-intelligence surface inside the
// container.
//
// The listener is reachable only through the project's own Docker network and a
// loopback-published port on the host, and the host gateway is the only intended
// caller. Scope is therefore structural: this process has exactly one workspace
// mounted, so there is no project identifier to check and no way to name another
// project in a request.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/searchindex"
	"github.com/lev-goryachev/lctk/images/code-intel/internal/symbols"
)

// Indexer is the subset of the store the API drives. It is an interface so the
// handler can be tested without building a real index.
type Indexer interface {
	State() (searchindex.State, error)
	Search(ctx context.Context, request searchindex.Request) (searchindex.Response, error)
	Rebuild(ctx context.Context) (searchindex.State, error)
	Reconcile(ctx context.Context) (searchindex.State, searchindex.Applied, error)
	Update(ctx context.Context, changes []searchindex.Change) (searchindex.State, searchindex.Applied, error)
	WatchSet(ctx context.Context) ([]string, bool, error)
	DiskBytes() (int64, error)
	// ReadProjectFile hands back one named file the project's own rules allow to be
	// read. The store decides that, not this layer.
	ReadProjectFile(relative string, maxBytes int64) ([]byte, string, error)
	// FilesContainingWord narrows a project-wide identifier lookup to the files
	// worth parsing, using the index.
	FilesContainingWord(ctx context.Context, word string, maxFiles int) ([]string, bool, error)
}

// Outliner extracts declarations from content. It receives bytes rather than a
// path, which is what keeps the decision about what may be read in one place.
type Outliner interface {
	Outline(ctx context.Context, name string, content []byte, digest string) (symbols.Outline, error)
	Locate(ctx context.Context, name string, content []byte, digest, wanted string) (symbols.Located, error)
	Languages() []string
	MaxBytes() int64
	// Parallelism is how many files may be parsed at once, reported so the policy in
	// force is visible rather than inferred from a refusal.
	Parallelism() int
}

// Server exposes the indexer over HTTP.
type Server struct {
	indexer Indexer
	// outliner is optional. A build without one serves every other route and
	// answers the outline route with a typed refusal, which is better than a
	// route that exists and returns nothing useful.
	outliner Outliner
	logger   *slog.Logger

	// indexing serializes every operation that publishes a generation. Two
	// concurrent builds would race to publish and could leave the newer
	// generation shadowed by the older one.
	indexing sync.Mutex
	progress atomic
}

// atomic tracks whether an index build is in flight, so a caller that gets
// INDEX_NOT_READY can tell "starting up" from "nothing is happening".
type atomic struct {
	mu      sync.RWMutex
	running bool
	since   time.Time
	lastErr string
}

func (a *atomic) start() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = true
	a.since = time.Now().UTC()
	a.lastErr = ""
}

func (a *atomic) finish(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	if err != nil {
		a.lastErr = err.Error()
	}
}

func (a *atomic) snapshot() (bool, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.running, a.lastErr
}

// New builds a server.
func New(indexer Indexer, outliner Outliner, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{indexer: indexer, outliner: outliner, logger: logger}
}

// Handler returns the routed HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /search", s.handleSearch)
	mux.HandleFunc("POST /index", s.handleIndex)
	mux.HandleFunc("GET /watchset", s.handleWatchSet)
	mux.HandleFunc("POST /outline", s.handleOutline)
	mux.HandleFunc("POST /locate", s.handleLocate)
	return mux
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// StatusView is what the host reads to describe index freshness.
type StatusView struct {
	Ready      bool   `json:"ready"`
	Indexing   bool   `json:"indexing"`
	Generation uint64 `json:"generation"`
	FileCount  int    `json:"file_count"`
	SkippedBig int    `json:"skipped_too_large"`
	// SkippedIgnored counts entries the project's own ignore rules excluded, so
	// a caller can tell "the project has 155 files" from "LCTK only looked at 155".
	SkippedIgnored int `json:"skipped_ignored"`
	// IgnoreSources names the ignore files in effect, so an operator can see
	// which rules produced the file count rather than inferring it.
	IgnoreSources []string `json:"ignore_sources,omitempty"`
	DeltaDepth    int      `json:"delta_depth"`
	// SourceBytes and IndexBytes are what the project costs: how much source is
	// indexed, and how much disk the index occupies. Reported together because
	// neither is meaningful alone -- a large index is only worrying next to the
	// source it describes.
	SourceBytes int64  `json:"source_bytes"`
	IndexBytes  int64  `json:"index_bytes"`
	IndexedAt   string `json:"indexed_at,omitempty"`
	// Reason explains a not-ready state, so the condition is diagnosable without
	// reading container logs.
	Reason string `json:"reason,omitempty"`
	// OutlineLanguages names what this build can outline, so a caller learns the
	// boundary by asking rather than by being refused.
	OutlineLanguages []string `json:"outline_languages,omitempty"`
	// SymbolParallelism is how many files may be parsed at once, zero meaning
	// unbounded. It is reported because it is a policy an operator sets and a figure
	// that explains a PARSE_BUSY refusal.
	SymbolParallelism int `json:"symbol_parallelism,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	running, lastErr := s.progress.snapshot()
	view := StatusView{Indexing: running, Reason: lastErr}
	if s.outliner != nil {
		// Reported before the index state is resolved, because what this build can
		// outline does not depend on whether an index exists.
		view.OutlineLanguages = s.outliner.Languages()
		view.SymbolParallelism = s.outliner.Parallelism()
	}

	state, err := s.indexer.State()
	if err != nil {
		var typed *searchindex.Error
		if errors.As(err, &typed) && view.Reason == "" {
			view.Reason = typed.Message
		}
		writeJSON(w, http.StatusOK, view)
		return
	}

	view.Ready = true
	if bytes, err := s.indexer.DiskBytes(); err == nil {
		view.IndexBytes = bytes
	}
	view.SourceBytes = state.SourceBytes
	view.Generation = state.Generation
	view.FileCount = state.FileCount
	view.SkippedBig = state.SkippedBig
	view.SkippedIgnored = state.SkippedIgnored
	view.IgnoreSources = state.IgnoreSources
	view.DeltaDepth = state.DeltaDepth
	view.IndexedAt = state.BuiltAt.Format(time.RFC3339)
	writeJSON(w, http.StatusOK, view)
}

// WatchSetView tells the host which directories it must watch to see every
// change that could reach this index.
type WatchSetView struct {
	Directories []string `json:"directories"`
	Count       int      `json:"count"`
	// Truncated reports that the project has more directories than the service
	// will describe. The caller must read it as "this set is incomplete", not as
	// the whole project, and fall back to reconciliation.
	Truncated bool `json:"truncated"`
	Limit     int  `json:"limit"`
}

func (s *Server) handleWatchSet(w http.ResponseWriter, r *http.Request) {
	directories, truncated, err := s.indexer.WatchSet(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, WatchSetView{
		Directories: directories,
		Count:       len(directories),
		Truncated:   truncated,
		Limit:       searchindex.MaxWatchDirectories,
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var request searchindex.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		writeTyped(w, searchindex.CodeInvalidPattern, "The request body is not valid JSON.", false, http.StatusBadRequest)
		return
	}

	response, err := s.indexer.Search(r.Context(), request)
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type outlineRequest struct {
	Path string `json:"path"`
}

// handleOutline answers with one file's declarations.
//
// It reads the file rather than the index, and that is the contract: an outline
// describes the file as it is on disk right now. There is nothing to flush and no
// generation to be behind, which is why the answer carries a content digest
// instead of an index generation.
func (s *Server) handleOutline(w http.ResponseWriter, r *http.Request) {
	if s.outliner == nil {
		writeTyped(w, symbols.CodeUnsupportedLanguage,
			"This build does not provide symbol outlines.", false, http.StatusBadRequest)
		return
	}

	var request outlineRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		writeTyped(w, searchindex.CodeInvalidPath, "The request body is not valid JSON.", false, http.StatusBadRequest)
		return
	}

	content, digest, err := s.indexer.ReadProjectFile(request.Path, s.outliner.MaxBytes())
	if err != nil {
		s.writeError(w, err)
		return
	}
	outline, err := s.outliner.Outline(r.Context(), request.Path, content, digest)
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, outline)
}

type locateRequest struct {
	Name string `json:"name"`
	// DeclarationsOnly keeps only the places the name is declared.
	DeclarationsOnly bool `json:"declarations_only,omitempty"`
	// MaxFiles bounds the work, since every candidate file is parsed.
	MaxFiles int `json:"max_files,omitempty"`
}

// LocateView is one identifier lookup across the project.
type LocateView struct {
	Name string `json:"name"`
	// Generation is the index generation that chose the candidate files. Unlike an
	// outline, this answer depends on the index: a file the index has not caught up
	// with is a file this lookup never opens.
	Generation uint64 `json:"generation"`
	IndexedAt  string `json:"indexed_at,omitempty"`
	// Files carries one entry per file with at least one occurrence.
	Files []symbols.Located `json:"files"`
	// Occurrences and Declarations are the totals across those files.
	Occurrences  int `json:"occurrences"`
	Declarations int `json:"declarations"`
	// FilesConsidered is how many files the index offered, which is not how many
	// appear in Files: a candidate whose only hits were in comments contributes
	// nothing, and that is the difference this lookup exists to make.
	FilesConsidered int `json:"files_considered"`
	// FilesTruncated says the candidate list was cut short, so "no other
	// references" is not a conclusion a caller may draw.
	FilesTruncated bool `json:"files_truncated,omitempty"`
	// SkippedUnsupported counts candidate files in a language this build has no
	// grammar for. They are counted rather than dropped, because a caller reading
	// an answer that silently omitted half the project would be misled.
	SkippedUnsupported int `json:"skipped_unsupported,omitempty"`
	// SkippedUnreadable counts candidates that could not be read or parsed, for the
	// same reason.
	SkippedUnreadable int `json:"skipped_unreadable,omitempty"`
}

// handleLocate finds every occurrence of one identifier across the project.
//
// The index narrows the work and the parser decides what counts. That split is the
// whole design: a text search knows which files hold the letters, and only a syntax
// tree knows whether they are an identifier or prose.
func (s *Server) handleLocate(w http.ResponseWriter, r *http.Request) {
	if s.outliner == nil {
		writeTyped(w, symbols.CodeUnsupportedLanguage,
			"This build does not provide symbol lookups.", false, http.StatusBadRequest)
		return
	}

	var request locateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		writeTyped(w, searchindex.CodeInvalidPattern, "The request body is not valid JSON.", false, http.StatusBadRequest)
		return
	}

	candidates, truncated, err := s.indexer.FilesContainingWord(r.Context(), request.Name, request.MaxFiles)
	if err != nil {
		s.writeError(w, err)
		return
	}

	view := LocateView{
		Name:            request.Name,
		Files:           []symbols.Located{},
		FilesConsidered: len(candidates),
		FilesTruncated:  truncated,
	}
	if state, err := s.indexer.State(); err == nil {
		view.Generation = state.Generation
		view.IndexedAt = state.BuiltAt.Format(time.RFC3339)
	}

	for _, candidate := range candidates {
		if err := r.Context().Err(); err != nil {
			s.writeError(w, err)
			return
		}
		content, digest, err := s.indexer.ReadProjectFile(candidate, s.outliner.MaxBytes())
		if err != nil {
			// A candidate that vanished between the index and this read, or one too
			// large to parse, is counted rather than allowed to fail the lookup: one
			// awkward file must not deny the answer about every other one.
			view.SkippedUnreadable++
			continue
		}
		located, err := s.outliner.Locate(r.Context(), candidate, content, digest, request.Name)
		if err != nil {
			var typed typedFailure
			if errors.As(err, &typed) {
				if code, _, _ := typed.Failure(); code == symbols.CodeUnsupportedLanguage {
					view.SkippedUnsupported++
					continue
				}
			}
			view.SkippedUnreadable++
			continue
		}
		if request.DeclarationsOnly {
			located.Occurrences = onlyDeclarations(located.Occurrences)
		}
		if len(located.Occurrences) == 0 {
			continue
		}
		view.Files = append(view.Files, located)
		view.Occurrences += len(located.Occurrences)
		view.Declarations += located.Declarations
	}
	writeJSON(w, http.StatusOK, view)
}

func onlyDeclarations(occurrences []symbols.Occurrence) []symbols.Occurrence {
	kept := make([]symbols.Occurrence, 0, len(occurrences))
	for _, occurrence := range occurrences {
		if occurrence.Declaration {
			kept = append(kept, occurrence)
		}
	}
	return kept
}

type indexRequest struct {
	// Mode is "reconcile" by default, "full" to force a complete rebuild, or
	// "apply" to submit an explicit change batch.
	Mode    string               `json:"mode,omitempty"`
	Changes []searchindex.Change `json:"changes,omitempty"`
}

type indexResponse struct {
	Generation uint64 `json:"generation"`
	FileCount  int    `json:"file_count"`
	// Applied counts the paths that actually changed in the index, which is not
	// the number submitted: a write whose content already matched is dropped.
	Applied int `json:"applied"`
	// Unchanged counts submitted writes that changed nothing, so a caller can see
	// the filter working instead of inferring it from a generation that held
	// still.
	Unchanged int    `json:"unchanged,omitempty"`
	FullBuild bool   `json:"full_build"`
	IndexedAt string `json:"indexed_at"`
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	var request indexRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
			writeTyped(w, searchindex.CodeInvalidPattern, "The request body is not valid JSON.", false, http.StatusBadRequest)
			return
		}
	}

	s.indexing.Lock()
	defer s.indexing.Unlock()
	s.progress.start()

	var (
		state   searchindex.State
		applied searchindex.Applied
		err     error
	)
	switch request.Mode {
	case "", "reconcile":
		state, applied, err = s.indexer.Reconcile(r.Context())
	case "full":
		state, err = s.indexer.Rebuild(r.Context())
		applied = searchindex.Applied{Changed: state.FileCount, Rebuilt: true, Generations: 1}
	case "apply":
		state, applied, err = s.indexer.Update(r.Context(), request.Changes)
	default:
		s.progress.finish(nil)
		writeTyped(w, searchindex.CodeInvalidPattern,
			"Unknown index mode; use reconcile, full, or apply.", false, http.StatusBadRequest)
		return
	}
	s.progress.finish(err)

	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, indexResponse{
		Generation: state.Generation,
		FileCount:  state.FileCount,
		Applied:    applied.Changed,
		Unchanged:  applied.Unchanged,
		// Whether *this request* rebuilt the index, which the published state
		// cannot answer: a batch that changed nothing returns the previous state,
		// and that state may well have been a full build.
		FullBuild: applied.Rebuilt,
		IndexedAt: state.BuiltAt.Format(time.RFC3339),
	})
}

// EnsureIndexed brings the index up to date, building it when absent.
//
// It runs at startup rather than on the first query, because the first query is
// the worst possible time to discover that a project needs a full build.
func (s *Server) EnsureIndexed(ctx context.Context) error {
	s.indexing.Lock()
	defer s.indexing.Unlock()
	s.progress.start()

	state, applied, err := s.indexer.Reconcile(ctx)
	s.progress.finish(err)
	if err != nil {
		return err
	}
	s.logger.Info("index ready",
		slog.Uint64("generation", state.Generation),
		slog.Int("files", state.FileCount),
		slog.Int("caught_up", applied.Changed),
		slog.Bool("full_build", applied.Rebuilt))
	return nil
}

// typedFailure is any of this service's own errors.
//
// The interface exists so this layer does not switch on which package produced a
// failure. More than one package now answers requests here, and a handler that
// knows their names would have to be edited every time another one is added --
// which is exactly how an error stops being typed and becomes "the request
// failed".
type typedFailure interface {
	error
	Failure() (code string, message string, retryable bool)
}

// writeError maps a typed failure onto a status code.
//
// The mapping is small on purpose. The typed code is what the caller acts on; the
// status code exists so ordinary HTTP tooling behaves sensibly.
func (s *Server) writeError(w http.ResponseWriter, err error) {
	var typed typedFailure
	if !errors.As(err, &typed) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeTyped(w, searchindex.CodeInternalError, "The request was cancelled.", true, http.StatusRequestTimeout)
			return
		}
		s.logger.Error("unclassified failure", slog.String("error", err.Error()))
		writeTyped(w, searchindex.CodeInternalError, "The request failed.", false, http.StatusInternalServerError)
		return
	}

	code, message, retryable := typed.Failure()
	status := http.StatusInternalServerError
	switch code {
	case searchindex.CodeIndexNotReady:
		status = http.StatusServiceUnavailable
	case searchindex.CodeIndexCorrupt:
		status = http.StatusInternalServerError
	case searchindex.CodeFileNotFound:
		status = http.StatusNotFound
	case searchindex.CodeInvalidPattern, searchindex.CodeInvalidCursor, searchindex.CodeLimitExceeded,
		searchindex.CodeInvalidPath, searchindex.CodeFileTooLarge,
		symbols.CodeUnsupportedLanguage, symbols.CodeParseIncomplete:
		status = http.StatusBadRequest
	}
	writeTyped(w, code, message, retryable, status)
}

func writeTyped(w http.ResponseWriter, code, message string, retryable bool, status int) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Retryable: retryable}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
