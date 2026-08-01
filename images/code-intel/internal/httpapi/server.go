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
)

// Indexer is the subset of the store the API drives. It is an interface so the
// handler can be tested without building a real index.
type Indexer interface {
	State() (searchindex.State, error)
	Search(ctx context.Context, request searchindex.Request) (searchindex.Response, error)
	Rebuild(ctx context.Context) (searchindex.State, error)
	Reconcile(ctx context.Context) (searchindex.State, []searchindex.Change, error)
	Update(ctx context.Context, changes []searchindex.Change) (searchindex.State, error)
}

// Server exposes the indexer over HTTP.
type Server struct {
	indexer Indexer
	logger  *slog.Logger

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
func New(indexer Indexer, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{indexer: indexer, logger: logger}
}

// Handler returns the routed HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /search", s.handleSearch)
	mux.HandleFunc("POST /index", s.handleIndex)
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
	IndexedAt     string   `json:"indexed_at,omitempty"`
	// Reason explains a not-ready state, so the condition is diagnosable without
	// reading container logs.
	Reason string `json:"reason,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	running, lastErr := s.progress.snapshot()
	view := StatusView{Indexing: running, Reason: lastErr}

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
	view.Generation = state.Generation
	view.FileCount = state.FileCount
	view.SkippedBig = state.SkippedBig
	view.SkippedIgnored = state.SkippedIgnored
	view.IgnoreSources = state.IgnoreSources
	view.DeltaDepth = state.DeltaDepth
	view.IndexedAt = state.BuiltAt.Format(time.RFC3339)
	writeJSON(w, http.StatusOK, view)
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

type indexRequest struct {
	// Mode is "reconcile" by default, "full" to force a complete rebuild, or
	// "apply" to submit an explicit change batch.
	Mode    string               `json:"mode,omitempty"`
	Changes []searchindex.Change `json:"changes,omitempty"`
}

type indexResponse struct {
	Generation uint64 `json:"generation"`
	FileCount  int    `json:"file_count"`
	Applied    int    `json:"applied"`
	FullBuild  bool   `json:"full_build"`
	IndexedAt  string `json:"indexed_at"`
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
		applied int
		err     error
	)
	switch request.Mode {
	case "", "reconcile":
		var changes []searchindex.Change
		state, changes, err = s.indexer.Reconcile(r.Context())
		applied = len(changes)
	case "full":
		state, err = s.indexer.Rebuild(r.Context())
		applied = state.FileCount
	case "apply":
		state, err = s.indexer.Update(r.Context(), request.Changes)
		applied = len(request.Changes)
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
		Applied:    applied,
		FullBuild:  state.FullBuild,
		IndexedAt:  state.BuiltAt.Format(time.RFC3339),
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

	state, changes, err := s.indexer.Reconcile(ctx)
	s.progress.finish(err)
	if err != nil {
		return err
	}
	s.logger.Info("index ready",
		slog.Uint64("generation", state.Generation),
		slog.Int("files", state.FileCount),
		slog.Int("caught_up", len(changes)),
		slog.Bool("full_build", state.FullBuild))
	return nil
}

// writeError maps an adapter error onto a status code.
//
// The mapping is small on purpose. The typed code is what the caller acts on;
// the status code exists so ordinary HTTP tooling behaves sensibly.
func (s *Server) writeError(w http.ResponseWriter, err error) {
	var typed *searchindex.Error
	if !errors.As(err, &typed) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeTyped(w, searchindex.CodeInternalError, "The request was cancelled.", true, http.StatusRequestTimeout)
			return
		}
		s.logger.Error("unclassified failure", slog.String("error", err.Error()))
		writeTyped(w, searchindex.CodeInternalError, "The request failed.", false, http.StatusInternalServerError)
		return
	}

	status := http.StatusInternalServerError
	switch typed.Code {
	case searchindex.CodeIndexNotReady:
		status = http.StatusServiceUnavailable
	case searchindex.CodeIndexCorrupt:
		status = http.StatusInternalServerError
	case searchindex.CodeInvalidPattern, searchindex.CodeInvalidCursor, searchindex.CodeLimitExceeded:
		status = http.StatusBadRequest
	}
	writeTyped(w, typed.Code, typed.Message, typed.Retryable, status)
}

func writeTyped(w http.ResponseWriter, code, message string, retryable bool, status int) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Retryable: retryable}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
