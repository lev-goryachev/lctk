package sweexplore

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestInstalledLCTKPreflight is opt-in because it requires the real private
// runtime. Acceptance enables it with explicit paths and identities; ordinary
// unit tests remain hermetic.
func TestInstalledLCTKPreflight(t *testing.T) {
	if os.Getenv("LCTK_BENCHMARK_INTEGRATION") != "1" {
		t.Skip("installed LCTK integration is not enabled")
	}
	workspace := WorkspaceConfig{
		Root: os.Getenv("LCTK_BENCHMARK_ROOT"), ProjectID: os.Getenv("LCTK_BENCHMARK_PROJECT"),
		LCTKExecutable: os.Getenv("LCTK_BENCHMARK_EXECUTABLE"), ExpectedLCTKVersion: os.Getenv("LCTK_BENCHMARK_VERSION"),
		FreshnessTimeoutSeconds: 30,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	proof, err := WaitForLCTK(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if proof.ExactGeneration == 0 || proof.ExactGeneration != proof.SemanticGeneration || proof.ExactGeneration != proof.GraphGeneration || proof.ExactGeneration != proof.WatcherGeneration {
		t.Fatalf("freshness proof = %+v", proof)
	}
}
