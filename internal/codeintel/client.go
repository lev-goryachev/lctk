// Package codeintel is the host-side adapter to a project's code-intelligence
// service.
//
// It is the boundary [ADR-0004] requires: the types here are LCTK's public
// vocabulary, and no backend concept crosses into them. The search engine lives
// in a Linux container per [ADR-0011] and is never linked into this executable,
// so the only thing the host knows about it is this HTTP contract.
//
// [ADR-0004]: ../../docs/adr/0004-stable-aggregated-tool-api.md
// [ADR-0011]: ../../docs/adr/0011-zoekt-exact-search-backend.md
package codeintel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// SchemaVersion is the version of the public search request and response
// shapes. It is independent of the product version and of the backend, so a
// client can reason about the contract it is speaking.
const SchemaVersion = 1

// Backend names the engine behind the adapter. It is reported as provenance
// rather than used for dispatch: a caller may want to know what answered, but
// nothing in the protocol depends on the value.
const Backend = "zoekt"

// DefaultSearchTimeout bounds a search when the caller sets no deadline of its
// own. Index operations are deliberately not bounded here: a full rebuild of a
// large project legitimately takes minutes, and the caller that asked for one
// owns the deadline.
const DefaultSearchTimeout = 30 * time.Second

// DefaultWatchSetTimeout bounds the directory enumeration a watcher needs before
// it can start. It is longer than a search because it is a whole-tree walk, and
// shorter than an index build because nothing is being written.
const DefaultWatchSetTimeout = 2 * time.Minute

// Error codes the adapter reports. They are LCTK's, not the service's, though
// the two vocabularies deliberately coincide where the meaning is the same.
const (
	CodeIndexNotReady     = "INDEX_NOT_READY"
	CodeIndexCorrupt      = "INDEX_CORRUPT"
	CodeInvalidPattern    = "INVALID_PATTERN"
	CodeInvalidCursor     = "INVALID_CURSOR"
	CodeLimitExceeded     = "LIMIT_EXCEEDED"
	CodeServiceOffline    = "SERVICE_UNAVAILABLE"
	CodeInternalError     = "INTERNAL_ERROR"
	CodeSearchUnsupported = "SEARCH_UNAVAILABLE"
)

// Error is a typed failure a caller can act on.
type Error struct {
	Code      string
	Message   string
	Retryable bool
	Action    string
	Cause     error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Code + ": " + e.Message
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

// Request is the public search request.
type Request struct {
	Pattern       string   `json:"pattern"`
	Mode          string   `json:"mode,omitempty"`
	CaseSensitive bool     `json:"case_sensitive,omitempty"`
	PathGlobs     []string `json:"path_globs,omitempty"`
	Languages     []string `json:"languages,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	Cursor        string   `json:"cursor,omitempty"`
}

// Match is one result line, with a path relative to the project root.
type Match struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Preview string `json:"preview"`
	Match   string `json:"match"`
}

// Provenance says where an answer came from and how fresh it is, so a caller can
// judge it rather than assume it.
type Provenance struct {
	Backend         string `json:"backend"`
	SchemaVersion   int    `json:"schema_version"`
	IndexGeneration uint64 `json:"index_generation"`
	IndexedAt       string `json:"indexed_at,omitempty"`
	FileCount       int    `json:"file_count"`
	// Freshness is filled in by the caller that knows what the host has observed
	// since this generation was built. The adapter cannot judge it: the service
	// knows what it indexed, not what has changed on disk since.
	Freshness string `json:"freshness,omitempty"`
}

// Response is the public search response.
type Response struct {
	Matches    []Match    `json:"matches"`
	Total      int        `json:"total"`
	Truncated  bool       `json:"truncated"`
	NextCursor string     `json:"next_cursor,omitempty"`
	Provenance Provenance `json:"provenance"`
}

// Status is the project service's own view of its index.
type Status struct {
	Ready          bool     `json:"ready"`
	Indexing       bool     `json:"indexing"`
	Generation     uint64   `json:"generation"`
	FileCount      int      `json:"file_count"`
	SkippedBig     int      `json:"skipped_too_large"`
	SkippedIgnored int      `json:"skipped_ignored"`
	IgnoreSources  []string `json:"ignore_sources,omitempty"`
	DeltaDepth     int      `json:"delta_depth"`
	// SourceBytes and IndexBytes are what the project costs on disk.
	SourceBytes int64  `json:"source_bytes"`
	IndexBytes  int64  `json:"index_bytes"`
	IndexedAt   string `json:"indexed_at,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// WatchSet is the set of project-relative directories a host watcher must
// observe to see every change that could reach the index.
//
// The service computes it, because the service owns the exclusion policy. A
// host-side reimplementation would be a second answer to "what belongs to this
// project", and the one that drifted would be the watcher, quietly missing edits.
type WatchSet struct {
	Directories []string `json:"directories"`
	Count       int      `json:"count"`
	// Truncated says the project has more directories than the service will
	// describe, so the set is a prefix rather than the whole project. A caller
	// must fall back to reconciliation instead of trusting it.
	Truncated bool `json:"truncated"`
	Limit     int  `json:"limit"`
}

// Client talks to one project's service.
type Client struct {
	// Address is the loopback host:port the service is published on. It is
	// supplied per call site rather than stored globally because the runtime
	// assigns it and it changes across a restart.
	Address string
	HTTP    *http.Client
}

// New returns a client for a published service address.
//
// The HTTP client carries no overall timeout on purpose. A single deadline
// cannot suit both a search, which must not hang, and a full index rebuild,
// which is allowed to take minutes; each call site sets its own instead.
func New(address string) *Client {
	return &Client{
		Address: address,
		HTTP: &http.Client{
			// The service is on loopback and is not a general-purpose endpoint;
			// following a redirect out of it would be a bug, not a feature.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Search runs one query against the project service.
func (c *Client) Search(ctx context.Context, request Request) (Response, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultSearchTimeout)
		defer cancel()
	}

	var service serviceResponse
	if err := c.call(ctx, "/search", request, &service); err != nil {
		return Response{}, err
	}
	response := Response{
		Matches:    service.Matches,
		Total:      service.Total,
		Truncated:  service.Truncated,
		NextCursor: service.NextCursor,
		Provenance: Provenance{
			Backend:         Backend,
			SchemaVersion:   SchemaVersion,
			IndexGeneration: service.Generation,
			IndexedAt:       service.IndexedAt,
			FileCount:       service.FileCount,
		},
	}
	if response.Matches == nil {
		response.Matches = []Match{}
	}
	return response, nil
}

// Status reports the project service's index state.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	if err := c.get(ctx, "/status", &status); err != nil {
		return Status{}, err
	}
	return status, nil
}

// WatchSet asks the service which directories a host watcher should observe.
func (c *Client) WatchSet(ctx context.Context) (WatchSet, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultWatchSetTimeout)
		defer cancel()
	}

	var set WatchSet
	if err := c.get(ctx, "/watchset", &set); err != nil {
		return WatchSet{}, err
	}
	return set, nil
}

// Change is one file event for the index to apply.
type Change struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted,omitempty"`
	// Subtree says the path names a removed directory, which takes every file
	// beneath it. The service expands it, because only the service knows what was
	// in there: by the time the change is reported the directory is gone.
	Subtree bool `json:"subtree,omitempty"`
}

// IndexResult is what an index operation published.
type IndexResult struct {
	Generation uint64 `json:"generation"`
	FileCount  int    `json:"file_count"`
	Applied    int    `json:"applied"`
	// FullBuild says the service decided to rebuild rather than apply a delta,
	// which it does for a batch large enough that a delta would cost more.
	FullBuild bool   `json:"full_build"`
	IndexedAt string `json:"indexed_at"`
}

// Apply submits an explicit batch of changes.
//
// The caller owns the deadline. A batch the service escalates to a full rebuild
// takes as long as a rebuild takes, and there is no useful single timeout that
// suits both that and a two-file edit.
func (c *Client) Apply(ctx context.Context, changes []Change) (IndexResult, error) {
	var result IndexResult
	body := struct {
		Mode    string   `json:"mode"`
		Changes []Change `json:"changes"`
	}{Mode: "apply", Changes: changes}
	if err := c.call(ctx, "/index", body, &result); err != nil {
		return IndexResult{}, err
	}
	return result, nil
}

// Reconcile asks the service to compare the workspace with its own inventory and
// apply the difference. It is the recovery path when the host's record of changes
// is known to be incomplete.
func (c *Client) Reconcile(ctx context.Context, full bool) (IndexResult, error) {
	mode := "reconcile"
	if full {
		mode = "full"
	}
	var result IndexResult
	if err := c.call(ctx, "/index", map[string]string{"mode": mode}, &result); err != nil {
		return IndexResult{}, err
	}
	return result, nil
}

// Reindex asks the service to catch up and reports the resulting status.
func (c *Client) Reindex(ctx context.Context, full bool) (Status, error) {
	if _, err := c.Reconcile(ctx, full); err != nil {
		return Status{}, err
	}
	return c.Status(ctx)
}

type serviceResponse struct {
	Matches    []Match `json:"matches"`
	Total      int     `json:"total"`
	Truncated  bool    `json:"truncated"`
	NextCursor string  `json:"next_cursor"`
	Generation uint64  `json:"generation"`
	IndexedAt  string  `json:"indexed_at"`
	FileCount  int     `json:"file_count"`
}

type serviceError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

func (c *Client) call(ctx context.Context, path string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return &Error{Code: CodeInternalError, Message: "The request could not be encoded.", Cause: err}
	}
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(encoded), out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	if strings.TrimSpace(c.Address) == "" {
		// A running project always has a published address. An empty one means
		// the stack predates the published port, which a restart fixes.
		return &Error{
			Code:      CodeSearchUnsupported,
			Message:   "This project's container does not publish a code-intelligence service.",
			Retryable: false,
			Action:    "Restart the project with lctk project restart so it is recreated with the current stack definition.",
		}
	}

	request, err := http.NewRequestWithContext(ctx, method, "http://"+c.Address+path, body)
	if err != nil {
		return &Error{Code: CodeInternalError, Message: "The request could not be built.", Cause: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	client := c.HTTP
	if client == nil {
		client = &http.Client{}
	}
	response, err := client.Do(request)
	if err != nil {
		return c.transportError(ctx, err)
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return &Error{Code: CodeInternalError, Message: "The project service response could not be read.",
			Retryable: true, Cause: err}
	}

	if response.StatusCode != http.StatusOK {
		return decodeServiceError(response.StatusCode, payload)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return &Error{Code: CodeInternalError,
			Message: "The project service returned a response this build does not understand.", Cause: err}
	}
	return nil
}

// transportError distinguishes a cancelled call from a service that is not
// answering, because the two lead a caller to do different things.
func (c *Client) transportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &Error{Code: CodeInternalError, Message: "The search was cancelled.", Retryable: true, Cause: ctxErr}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{
			Code:      CodeServiceOffline,
			Message:   "The project's code-intelligence service did not answer in time.",
			Retryable: true,
			Action:    "Retry shortly; a first index build on a large project can be slow.",
			Cause:     err,
		}
	}
	return &Error{
		Code:      CodeServiceOffline,
		Message:   "The project's code-intelligence service is not reachable.",
		Retryable: true,
		Action:    "Check the project with lctk project status.",
		Cause:     err,
	}
}

func decodeServiceError(status int, payload []byte) error {
	var envelope serviceError
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Error.Code == "" {
		return &Error{
			Code:      CodeInternalError,
			Message:   fmt.Sprintf("The project service failed with status %d.", status),
			Retryable: status >= http.StatusInternalServerError,
		}
	}

	typed := &Error{
		Code:      envelope.Error.Code,
		Message:   envelope.Error.Message,
		Retryable: envelope.Error.Retryable,
	}
	switch typed.Code {
	case CodeIndexNotReady:
		typed.Action = "The project is still building its index; retry shortly."
	case CodeIndexCorrupt:
		typed.Action = "Rebuild the index with lctk project reindex --full."
	case CodeInvalidPattern, CodeInvalidCursor, CodeLimitExceeded:
		typed.Action = "Correct the request and try again."
	}
	return typed
}
