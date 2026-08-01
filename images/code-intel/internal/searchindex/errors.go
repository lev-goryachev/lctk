// Package searchindex is LCTK's owned adapter over the Zoekt search engine.
//
// It is the only place in LCTK that knows Zoekt exists. Nothing Zoekt-specific
// crosses this package boundary: callers hand it a [Request] and receive a
// [Response], both of which are LCTK's own shapes. That is what makes the engine
// replaceable without touching the public tool schema, which is the whole point
// of [ADR-0011] and [ADR-0004].
//
// The package maintains its own file inventory rather than reading Git objects,
// so a file that is saved but neither committed nor added is still searchable.
//
// [ADR-0011]: ../../../../docs/adr/0011-zoekt-exact-search-backend.md
// [ADR-0004]: ../../../../docs/adr/0004-stable-aggregated-tool-api.md
package searchindex

import "fmt"

// Error codes. They are the vocabulary the host adapter translates into typed
// MCP errors, so they are part of this package's contract and not free text.
const (
	// CodeIndexNotReady reports that no generation has been published yet.
	CodeIndexNotReady = "INDEX_NOT_READY"
	// CodeIndexCorrupt reports persistent state that cannot be used and will not
	// repair itself.
	CodeIndexCorrupt = "INDEX_CORRUPT"
	// CodeInvalidPattern reports a pattern, glob, or language the caller can fix.
	CodeInvalidPattern = "INVALID_PATTERN"
	// CodeInvalidCursor reports a pagination cursor that is malformed or belongs
	// to a generation that no longer exists.
	CodeInvalidCursor = "INVALID_CURSOR"
	// CodeLimitExceeded reports a requested result limit above the maximum.
	CodeLimitExceeded = "LIMIT_EXCEEDED"
	// CodeInternalError reports a backend failure the caller cannot act on.
	CodeInternalError = "INTERNAL_ERROR"
)

// Error is a typed failure.
//
// Retryable is carried rather than derived, because only the code that produced
// the failure knows whether waiting helps, and the caller acting on it is an
// agent that has to decide whether to try again.
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

func fail(code, message string, retryable bool, cause error) error {
	return &Error{Code: code, Message: message, Retryable: retryable, Cause: cause}
}

func internal(message string, cause error) error {
	return fail(CodeInternalError, message, false, cause)
}
