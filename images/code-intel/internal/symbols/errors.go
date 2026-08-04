package symbols

import "fmt"

// Error codes. They are the vocabulary the host adapter translates into typed MCP
// errors, so they are part of this package's contract rather than free text.
const (
	// CodeUnsupportedLanguage reports a file this build has no grammar for. It is a
	// stated gap: the alternative is an empty outline, which reads as "this file
	// declares nothing".
	CodeUnsupportedLanguage = "LANGUAGE_UNSUPPORTED"
	// CodeParseIncomplete reports a file the parser did not finish inside its
	// budget. It is not retryable: the same file will exhaust the same budget.
	CodeParseIncomplete = "PARSE_INCOMPLETE"
	// CodeInternalError reports a failure the caller cannot act on.
	CodeInternalError = "INTERNAL_ERROR"
)

// Error is a typed failure.
//
// Retryable is carried rather than derived, because only the code that produced
// the failure knows whether waiting helps, and the caller acting on it is an agent
// deciding whether to try again.
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

// Failure exposes the typed parts without the caller having to know which package
// produced the error. The HTTP layer serves more than one of these packages and
// has no business switching on which.
func (e *Error) Failure() (string, string, bool) { return e.Code, e.Message, e.Retryable }

func fail(code, message string, retryable bool, cause error) error {
	return &Error{Code: code, Message: message, Retryable: retryable, Cause: cause}
}
