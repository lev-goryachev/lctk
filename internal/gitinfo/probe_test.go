package gitinfo

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// TestProbeThisRepository is a diagnostic, not an assertion. It prints what the
// parser makes of the repository the tests are running in, which is how the
// parser was checked against real output rather than against a fixture of what
// the output was assumed to be.
func TestProbeThisRepository(t *testing.T) {
	if os.Getenv("LCTK_GIT_PROBE") == "" {
		t.Skip("set LCTK_GIT_PROBE=1 to print the live status")
	}
	status, err := New().Status(context.Background(), "../..", Options{IncludeUntracked: true})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.MarshalIndent(status, "", "  ")
	t.Log("\n" + string(encoded))
}
