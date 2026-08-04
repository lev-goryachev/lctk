// Package semantic owns the persistent semantic index for exactly one project.
//
// The package receives project-relative files through an authority supplied by
// the exact index. It never opens the workspace itself and therefore cannot
// widen project scope or bypass ignore rules.
package semantic

import "fmt"

// Error codes form the private service contract translated by the host into
// public MCP failures.
const (
	CodeNotReady             = "SEMANTIC_NOT_READY"
	CodeEmbeddingUnavailable = "EMBEDDING_UNAVAILABLE"
	CodeCorrupt              = "SEMANTIC_CORRUPT"
	CodeModelMismatch        = "MODEL_MISMATCH"
	CodeInvalidQuery         = "INVALID_QUERY"
	CodeInternalError        = "INTERNAL_ERROR"
)

// Error is a typed semantic failure. Retryability is decided where the failure
// happens because a missing inference service and an incompatible model require
// different client actions even though both prevent a query.
type Error struct {
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

// Error implements error without leaking an unclassified backend message into
// the stable response vocabulary.
func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Code + ": " + e.Message
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
}

// Unwrap preserves the original failure for logs and tests.
func (e *Error) Unwrap() error { return e.Cause }

// Failure lets the shared HTTP layer serve this package without importing its
// concrete error type.
func (e *Error) Failure() (string, string, bool) { return e.Code, e.Message, e.Retryable }

func fail(code, message string, retryable bool, cause error) error {
	return &Error{Code: code, Message: message, Retryable: retryable, Cause: cause}
}
