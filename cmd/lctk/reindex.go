package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

// reindexTimeout bounds a rebuild. A full build of a large project is slow, so
// this is generous; the alternative, an unbounded wait, hides a hung service.
const reindexTimeout = 10 * time.Minute

type reindexView struct {
	ProjectID  string `json:"project_id"`
	Full       bool   `json:"full"`
	Ready      bool   `json:"ready"`
	Indexing   bool   `json:"indexing"`
	Generation uint64 `json:"generation"`
	FileCount  int    `json:"file_count"`
	SkippedBig int    `json:"skipped_too_large"`
	IndexedAt  string `json:"indexed_at,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// runProjectReindex asks a running project to bring its index up to date.
//
// Slice 2.1 adds a watcher that does this continuously. Until then this is the
// explicit way to catch up, and it is also the documented recovery for a corrupt
// index, which is why the typed error from the adapter names it.
func runProjectReindex(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("project reindex", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the result as JSON")
	full := flags.Bool("full", false, "discard the existing index and build it again from scratch")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk project reindex [--full] [--json] PROJECT")
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}
	project, err := registry.Resolve(flags.Arg(0))
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), reindexTimeout)
	defer cancel()

	status, err := newStackManager().Status(ctx, project)
	if err != nil {
		return err
	}
	if status.ServiceAddress == "" {
		return fmt.Errorf("project %s is not running a code-intelligence service; start it with lctk project start",
			project.ID)
	}

	indexStatus, err := codeintel.New(status.ServiceAddress).Reindex(ctx, *full)
	if err != nil {
		return err
	}

	view := reindexView{
		ProjectID:  project.ID,
		Full:       *full,
		Ready:      indexStatus.Ready,
		Indexing:   indexStatus.Indexing,
		Generation: indexStatus.Generation,
		FileCount:  indexStatus.FileCount,
		SkippedBig: indexStatus.SkippedBig,
		IndexedAt:  indexStatus.IndexedAt,
		Reason:     indexStatus.Reason,
	}
	if *asJSON {
		return writeJSON(stdout, view)
	}

	fmt.Fprintf(stdout, "%s\n", view.ProjectID)
	fmt.Fprintf(stdout, "  generation: %d\n", view.Generation)
	fmt.Fprintf(stdout, "  files:      %d\n", view.FileCount)
	if view.SkippedBig > 0 {
		fmt.Fprintf(stdout, "  skipped:    %d file(s) over the size limit\n", view.SkippedBig)
	}
	if view.IndexedAt != "" {
		fmt.Fprintf(stdout, "  indexed at: %s\n", view.IndexedAt)
	}
	if view.Reason != "" {
		fmt.Fprintf(stdout, "  note:       %s\n", view.Reason)
	}
	return nil
}
