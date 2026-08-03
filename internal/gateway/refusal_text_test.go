package gateway

import (
	"strings"
	"testing"

	"github.com/lev-goryachev/lctk/internal/codeintel"
)

// A refusal reaches a model as one line of text, so the text is the interface.
// These check how it reads, which is not something the typed fields can express:
// every field can be correct and the sentence still be unparseable on first pass.
func TestARefusalReadsAsSentences(t *testing.T) {
	cases := []struct {
		name  string
		err   *searchToolError
		want  string
		avoid string
	}{
		{
			// The case found by reading real output: the message ends on a
			// backticked pattern, and without a terminator the advice looks like
			// part of the pattern.
			name: "a message ending on a quoted value is closed before the advice",
			err: &searchToolError{
				code:    codeintel.CodeInvalidPattern,
				message: "the regular expression is invalid: missing closing ]: `[unclosed`",
				action:  "Correct the request and try again.",
			},
			want:  "`[unclosed`. Correct",
			avoid: "`[unclosed` Correct",
		},
		{
			name: "a message that punctuates itself is left alone",
			err: &searchToolError{
				code:    codeintel.CodeSearchUnsupported,
				message: "This project does not expose a code-intelligence service.",
				action:  "Restart it with lctk project restart.",
			},
			want:  "service. Restart",
			avoid: "service.. Restart",
		},
		{
			// A colon would become ":." which is worse than the seam it fixes.
			name: "a message ending in a colon is not given a second mark",
			err: &searchToolError{
				code:    codeintel.CodeInvalidPattern,
				message: "the path glob must stay inside the project:",
				action:  "Use a project-relative glob.",
			},
			want:  "project: Use",
			avoid: "project:. Use",
		},
		{
			name: "an unpunctuated action is closed too",
			err: &searchToolError{
				code:    codeintel.CodeInternalError,
				message: "the service did not answer",
				action:  "Check the project with lctk project status",
			},
			want:  "lctk project status.",
			avoid: "answer Check",
		},
		{
			name: "a retryable refusal says so between the two",
			err: &searchToolError{
				code:      codeintel.CodeIndexNotReady,
				message:   "the index is still being built",
				action:    "Retry shortly",
				retryable: true,
			},
			want:  "built. This is retryable. Retry shortly.",
			avoid: "built This is",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.err.Error()
			if !strings.Contains(got, c.want) {
				t.Errorf("the refusal reads %q, which does not contain %q", got, c.want)
			}
			if strings.Contains(got, c.avoid) {
				t.Errorf("the refusal reads %q, which still contains %q", got, c.avoid)
			}
			if !strings.HasPrefix(got, c.err.code+": ") {
				t.Errorf("the refusal does not lead with its code: %q", got)
			}
		})
	}
}

// A stale cursor is not a malformed request. Telling a caller to correct one is
// misleading: nothing about it was wrong when it was made, the index moved
// underneath it, and the fix is to start over rather than adjust an argument.
func TestAStaleCursorIsToldToStartAgainRatherThanToCorrectItself(t *testing.T) {
	action := codeintel.ActionFor(codeintel.CodeInvalidCursor)
	if action == "" {
		t.Fatal("a stale cursor carries no recommended action")
	}
	if strings.Contains(action, "Correct the request") {
		t.Errorf("a stale cursor is told to correct the request: %q", action)
	}
	for _, want := range []string{"again", "generation"} {
		if !strings.Contains(action, want) {
			t.Errorf("the action %q does not mention %q, so it does not explain what to do or why", action, want)
		}
	}
}
