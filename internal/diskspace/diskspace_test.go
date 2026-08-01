package diskspace

import (
	"path/filepath"
	"testing"
)

func TestAvailableReportsAPlausibleFigure(t *testing.T) {
	free, err := Available(t.TempDir())
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if free == 0 {
		t.Fatal("free space reported as zero on a volume a test just wrote to")
	}
}

// The usual caller asks about a directory it is about to create, so a path that
// does not exist yet must still produce an answer.
func TestAvailableAnswersForAPathThatDoesNotExistYet(t *testing.T) {
	future := filepath.Join(t.TempDir(), "not", "created", "yet")
	free, err := Available(future)
	if err != nil {
		t.Fatalf("Available(%q): %v", future, err)
	}
	if free == 0 {
		t.Fatal("free space reported as zero")
	}
}

func TestTightIsAboutWhatIsLeftAfterwards(t *testing.T) {
	const gib = 1 << 30

	roomy := Estimate{ExpectedBytes: 1 * gib, AvailableBytes: 20 * gib}
	if roomy.Tight() {
		t.Error("20 GiB free for a 1 GiB index was called tight")
	}

	cramped := Estimate{ExpectedBytes: 4 * gib, AvailableBytes: 4 * gib}
	if !cramped.Tight() {
		t.Error("an index that would fill the volume exactly was not called tight")
	}

	// An index that already exists costs only the difference, so a project that
	// is merely being restarted must not be reported as short of room.
	built := Estimate{ExpectedBytes: 4 * gib, IndexBytes: 4 * gib, AvailableBytes: 2 * gib}
	if built.Tight() {
		t.Error("an already-built index was counted again against free space")
	}
}

func TestHumanReadsLikeASize(t *testing.T) {
	cases := map[int64]string{
		0:              "0 B",
		512:            "512 B",
		1536:           "1.5 KiB",
		3 << 20:        "3.0 MiB",
		int64(2) << 30: "2.0 GiB",
	}
	for bytes, want := range cases {
		if got := Human(bytes); got != want {
			t.Errorf("Human(%d) = %q, want %q", bytes, got, want)
		}
	}
}

// The model is anchored to a real measurement: this repository, 1.19 MiB of
// source, occupies 9.98 MiB of index. The estimate must land near that, or the
// warning it drives is noise.
func TestTheEstimateMatchesTheOneProjectItWasMeasuredOn(t *testing.T) {
	const measuredSource = 1_248_000 // 1.19 MiB
	const measuredIndex = 10_464_000 // 9.98 MiB

	estimated := ExpectedIndexBytes(measuredSource)
	ratio := float64(estimated) / float64(measuredIndex)
	if ratio < 0.75 || ratio > 1.5 {
		t.Fatalf("estimated %d for a project measured at %d (%.2fx off)",
			estimated, measuredIndex, ratio)
	}
}

// A project with no source measured yet has no estimate to give, and saying
// "zero" would be worse than saying nothing.
func TestNoSourceMeansNoEstimate(t *testing.T) {
	if got := ExpectedIndexBytes(0); got != 0 {
		t.Fatalf("ExpectedIndexBytes(0) = %d, want 0", got)
	}
}
