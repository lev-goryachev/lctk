package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/diskspace"
	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

// diskEstimateTimeout bounds the status call behind an estimate. The figure is
// advisory, so a slow service must delay a start by a moment, not block it.
const diskEstimateTimeout = 5 * time.Second

// estimateFor reports what a project costs on disk and what room is left.
//
// Free space is measured on the volume holding the LCTK home. That is where
// Docker Desktop keeps its data in a default installation on both target
// platforms, and the index lives in a Docker volume rather than in a directory
// LCTK owns, so there is nothing more direct to measure. An operator who has
// relocated Docker's data directory gets a proxy rather than a measurement, which
// is why the figure warns and never refuses on its own.
func estimateFor(ctx context.Context, project projectregistry.Project) (diskspace.Estimate, error) {
	home, err := lctkhome.Dir()
	if err != nil {
		return diskspace.Estimate{}, err
	}
	free, err := diskspace.Available(home)
	if err != nil {
		return diskspace.Estimate{}, err
	}

	estimate := diskspace.Estimate{AvailableBytes: free}

	status, err := newStackManager().Status(ctx, project)
	if err != nil || status.ServiceAddress == "" {
		return estimate, nil
	}

	indexCtx, cancel := context.WithTimeout(ctx, diskEstimateTimeout)
	defer cancel()
	indexStatus, err := codeintel.New(status.ServiceAddress).Status(indexCtx)
	if err != nil {
		return estimate, nil
	}

	estimate.SourceBytes = indexStatus.SourceBytes
	estimate.IndexBytes = indexStatus.IndexBytes
	estimate.ExpectedBytes = indexStatus.IndexBytes
	if estimate.ExpectedBytes == 0 {
		estimate.ExpectedBytes = diskspace.ExpectedIndexBytes(estimate.SourceBytes)
	}
	return estimate, nil
}

// warnIfDiskIsTight prints what starting will cost when the answer is worth
// hearing, and reports whether the caller should stop.
//
// It refuses rather than warns only when the volume is genuinely short, and even
// then the caller can say to proceed. The alternative — starting anyway — fails
// halfway through a build, leaves a partial generation, and gives the user two
// symptoms to untangle instead of one sentence beforehand.
func warnIfDiskIsTight(ctx context.Context, project projectregistry.Project, stdout io.Writer, proceed bool) error {
	estimate, err := estimateFor(ctx, project)
	if err != nil {
		// Not knowing how much room there is is not a reason to refuse to start.
		return nil
	}
	if !estimate.Tight() {
		return nil
	}

	fmt.Fprintf(stdout, "%s: disk is tight\n", project.ID)
	fmt.Fprintf(stdout, "  free:     %s\n", diskspace.Human(int64(estimate.AvailableBytes)))
	if estimate.IndexBytes > 0 {
		fmt.Fprintf(stdout, "  index:    %s already, for %s of source\n",
			diskspace.Human(estimate.IndexBytes), diskspace.Human(estimate.SourceBytes))
	} else if estimate.ExpectedBytes > 0 {
		fmt.Fprintf(stdout, "  expected: about %s of index for %s of source\n",
			diskspace.Human(estimate.ExpectedBytes), diskspace.Human(estimate.SourceBytes))
	}

	if proceed {
		fmt.Fprintf(stdout, "  starting anyway, as asked\n")
		return nil
	}
	return fmt.Errorf(
		"project %s would leave less than a gigabyte free; free some space, or pass --yes to start anyway",
		project.ID)
}

// diskView is the disk half of the resources report.
type diskView struct {
	SourceBytes    int64  `json:"source_bytes"`
	IndexBytes     int64  `json:"index_bytes"`
	ExpectedBytes  int64  `json:"expected_index_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	Tight          bool   `json:"tight"`
}

func diskViewOf(estimate diskspace.Estimate) diskView {
	return diskView{
		SourceBytes:    estimate.SourceBytes,
		IndexBytes:     estimate.IndexBytes,
		ExpectedBytes:  estimate.ExpectedBytes,
		AvailableBytes: estimate.AvailableBytes,
		Tight:          estimate.Tight(),
	}
}

func printDisk(stdout io.Writer, view diskView) {
	if view.IndexBytes > 0 {
		fmt.Fprintf(stdout, "  index:       %s on disk for %s of source\n",
			diskspace.Human(view.IndexBytes), diskspace.Human(view.SourceBytes))
	} else if view.ExpectedBytes > 0 {
		fmt.Fprintf(stdout, "  index:       not built; expect about %s for %s of source\n",
			diskspace.Human(view.ExpectedBytes), diskspace.Human(view.SourceBytes))
	} else {
		fmt.Fprintf(stdout, "  index:       not built, and its size is not yet known\n")
	}
	fmt.Fprintf(stdout, "  free space:  %s%s\n",
		diskspace.Human(int64(view.AvailableBytes)), tightSuffix(view.Tight))
}

func tightSuffix(tight bool) string {
	if tight {
		return " (tight)"
	}
	return ""
}
