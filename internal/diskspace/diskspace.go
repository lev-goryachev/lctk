// Package diskspace reports how much room is left where LCTK is about to spend
// it.
//
// It exists because an index that runs out of disk fails late and badly: a
// half-written generation, a container that will not start, and a user with no
// idea which of the two caused the other. Asking first turns that into a sentence.
package diskspace

import (
	"fmt"
	"os"
	"path/filepath"
)

// The index-size model: a fixed cost per project plus a share of the source.
//
// The shape comes from the format rather than from curve fitting. A shard carries
// metadata whose size does not depend on how much source it holds, and LCTK
// retains two generations, so a project pays that twice before it pays for any
// content. The content term is the trigram index, which does scale with source.
//
// The numbers are anchored to one measurement: this repository, 179 files and
// 1.19 MiB of source, occupies 9.98 MiB of index across two retained
// generations. A single small repository is not a sample, and on a project where
// the fixed cost no longer dominates this will overestimate. That is the safe
// direction — the figure only ever warns, and never refuses on its own — but it
// is a guess about large projects until one is measured.
const (
	IndexFixedBytes    = 8 << 20
	IndexToSourceRatio = 2.0
)

// ExpectedIndexBytes estimates what an index will occupy for a given amount of
// source.
func ExpectedIndexBytes(sourceBytes int64) int64 {
	if sourceBytes <= 0 {
		return 0
	}
	return IndexFixedBytes + int64(float64(sourceBytes)*IndexToSourceRatio)
}

// Available reports the free bytes on the volume holding a path.
//
// The path need not exist; the nearest existing ancestor is measured, because the
// usual caller is asking about a directory it is about to create.
func Available(path string) (uint64, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, fmt.Errorf("resolve %q: %w", path, err)
	}

	existing := absolute
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return 0, fmt.Errorf("no existing directory above %q", absolute)
		}
		existing = parent
	}
	return available(existing)
}

// Estimate is what a project is expected to cost on disk.
type Estimate struct {
	// SourceBytes is what the project's indexed files occupy, zero when it has
	// never been indexed and nothing better is known.
	SourceBytes int64
	// IndexBytes is what the index currently occupies, zero before a first build.
	IndexBytes int64
	// ExpectedBytes is what the index is expected to occupy once built. For a
	// project that already has one, it is what it already has.
	ExpectedBytes int64
	// AvailableBytes is the free space where the index lives.
	AvailableBytes uint64
}

// Tight reports whether the expected index leaves less than a comfortable margin.
//
// The margin is not about the index. A volume with a few hundred megabytes left
// after indexing is a volume about to cause trouble for everything else on the
// machine, and the person who asked for the index is the one in a position to
// decide that is acceptable.
func (e Estimate) Tight() bool {
	const margin = 1 << 30 // 1 GiB
	needed := e.ExpectedBytes - e.IndexBytes
	if needed < 0 {
		needed = 0
	}
	return e.AvailableBytes < uint64(needed)+margin
}

// Human renders a byte count the way a person reads one.
func Human(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value, exponent := float64(bytes)/unit, 0
	for value >= unit && exponent < 3 {
		value /= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGT"[exponent])
}
