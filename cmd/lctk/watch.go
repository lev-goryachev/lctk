package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lev-goryachev/lctk/internal/changejournal"
	"github.com/lev-goryachev/lctk/internal/codeintel"
	"github.com/lev-goryachev/lctk/internal/fswatch"
	"github.com/lev-goryachev/lctk/internal/hostsettings"
	"github.com/lev-goryachev/lctk/internal/projectmanifest"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

type watchView struct {
	ProjectID string `json:"project_id"`
	// Journal is the document the daemon writes, named so an operator can read it
	// directly when a status line is not enough.
	Journal string `json:"journal"`
	// Recorded says a journal exists. Without one the daemon has never observed
	// this project, which is different from having observed no changes.
	Recorded        bool               `json:"recorded"`
	Sequence        uint64             `json:"sequence"`
	Checkpoint      uint64             `json:"checkpoint"`
	Generation      uint64             `json:"generation"`
	Pending         int                `json:"pending"`
	Complete        bool               `json:"complete"`
	Gap             *changejournal.Gap `json:"gap,omitempty"`
	LastEventAt     string             `json:"last_event_at,omitempty"`
	UpdatedAt       string             `json:"updated_at,omitempty"`
	DebounceSeconds float64            `json:"debounce_seconds"`
	Changes         []changejournal.Entry
}

// runProjectWatch reports what the host watcher has recorded for a project, and
// can follow live events for diagnosis.
//
// The reported state is read from the journal on disk rather than from the
// daemon, so it answers even when no daemon is running — which is exactly when an
// operator wants to know what the last observation was.
func runProjectWatch(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("project watch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the result as JSON")
	follow := flags.Bool("follow", false, "stream normalized filesystem events until interrupted")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk project watch [--follow] [--json] PROJECT")
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}
	project, err := registry.Resolve(flags.Arg(0))
	if err != nil {
		return err
	}

	if *follow {
		return followProject(ctx, project.ID, project.Path, stdout)
	}

	view, err := watchStatus(project.ID, project.Path)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, view)
	}
	return printWatchView(stdout, view)
}

func watchStatus(projectID, projectRoot string) (watchView, error) {
	path, err := changejournal.PathFor(projectID)
	if err != nil {
		return watchView{}, err
	}

	view := watchView{
		ProjectID:       projectID,
		Journal:         path,
		Complete:        true,
		DebounceSeconds: resolveDebounce(projectRoot).Seconds(),
	}

	raw, err := readJournal(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return view, nil
		}
		return watchView{}, fmt.Errorf("read change journal %q: %w", path, err)
	}

	var snapshot changejournal.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return watchView{}, fmt.Errorf("change journal %q is not valid JSON: %w", path, err)
	}

	view.Recorded = true
	view.Sequence = snapshot.Sequence
	view.Checkpoint = snapshot.Checkpoint
	view.Generation = snapshot.Generation
	view.Pending = len(snapshot.Pending)
	view.Complete = snapshot.Gap == nil
	view.Gap = snapshot.Gap
	view.Changes = snapshot.Pending
	if !snapshot.LastEventAt.IsZero() {
		view.LastEventAt = snapshot.LastEventAt.UTC().Format(time.RFC3339)
	}
	if !snapshot.UpdatedAt.IsZero() {
		view.UpdatedAt = snapshot.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return view, nil
}

// readJournal reads the document, retrying briefly while the daemon replaces it.
//
// The daemon publishes a new journal by renaming a temporary file over the old
// one. On Windows that rename briefly locks the target, so a reader arriving at
// exactly the wrong moment is refused rather than given stale content. The fix is
// to wait: the window is a rename, not an operation, and it closes immediately.
func readJournal(path string) ([]byte, error) {
	const attempts = 10
	var err error
	for attempt := range attempts {
		var raw []byte
		raw, err = os.ReadFile(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return raw, err
		}
		if attempt < attempts-1 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	return nil, err
}

// resolveDebounce reports the window that would apply to this project, so the
// status line shows the policy in force rather than the shipped default.
func resolveDebounce(projectRoot string) time.Duration {
	settings, err := hostsettings.Load()
	if err != nil {
		settings = hostsettings.Defaults
	}
	watch := settings.Watch
	if manifest, err := projectmanifest.Load(projectRoot); err == nil {
		watch = watch.WithProjectDebounce(manifest.Manifest.Index.DebounceMS)
	}
	return watch.Debounce()
}

func printWatchView(stdout io.Writer, view watchView) error {
	fmt.Fprintf(stdout, "%s\n", view.ProjectID)
	if !view.Recorded {
		fmt.Fprintf(stdout, "  observation: never; the daemon has not watched this project\n")
		fmt.Fprintf(stdout, "  journal:     %s (absent)\n", view.Journal)
		return nil
	}

	fmt.Fprintf(stdout, "  pending:    %d change(s) since the index was last caught up\n", view.Pending)
	fmt.Fprintf(stdout, "  sequence:   %d observed, %d applied\n", view.Sequence, view.Checkpoint)
	if view.Generation > 0 {
		fmt.Fprintf(stdout, "  index:      generation %d\n", view.Generation)
	}
	if view.Complete {
		fmt.Fprintf(stdout, "  record:     complete\n")
	} else {
		fmt.Fprintf(stdout, "  record:     incomplete (%s)\n", view.Gap.Reason)
		if view.Gap.Detail != "" {
			fmt.Fprintf(stdout, "              %s\n", view.Gap.Detail)
		}
		fmt.Fprintf(stdout, "              a reconciliation is needed; the pending count is a lower bound\n")
	}
	fmt.Fprintf(stdout, "  debounce:   %.1fs after the last save\n", view.DebounceSeconds)
	if view.LastEventAt != "" {
		fmt.Fprintf(stdout, "  last event: %s\n", view.LastEventAt)
	}
	if view.UpdatedAt != "" {
		fmt.Fprintf(stdout, "  written:    %s\n", view.UpdatedAt)
	}
	fmt.Fprintf(stdout, "  journal:    %s\n", view.Journal)

	for i, entry := range view.Changes {
		if i == 10 {
			fmt.Fprintf(stdout, "  ... and %d more\n", len(view.Changes)-10)
			break
		}
		suffix := ""
		if entry.Directory {
			suffix = "/"
		}
		fmt.Fprintf(stdout, "    %-8s %s%s\n", entry.Kind, entry.Path, suffix)
	}
	return nil
}

// followProject streams normalized events for diagnosis.
//
// It starts a watcher of its own rather than subscribing to the daemon's, and
// records nothing: this is a way to see what the host actually observes, not a
// second writer to the journal.
func followProject(ctx context.Context, projectID, projectRoot string, stdout io.Writer) error {
	directories, err := watchSetFor(ctx, projectID)
	if err != nil {
		return err
	}

	watcher, err := fswatch.Start(fswatch.Options{Root: projectRoot, Directories: directories})
	if err != nil {
		return err
	}
	defer watcher.Close()

	signalled, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stdout, "watching %s (%d directories); this does not write to the journal\n",
		projectRoot, watcher.Watched())
	for {
		select {
		case <-signalled.Done():
			return nil
		case event, ok := <-watcher.Events():
			if !ok {
				return nil
			}
			suffix := ""
			if event.Directory {
				suffix = "/"
			}
			fmt.Fprintf(stdout, "%s  %-8s %s%s\n",
				event.At.Format(time.RFC3339), event.Kind, event.Path, suffix)
		case gap, ok := <-watcher.Gaps():
			if !ok {
				return nil
			}
			fmt.Fprintf(stdout, "%s  gap      %s: %s\n",
				gap.At.Format(time.RFC3339), gap.Reason, gap.Detail)
		}
	}
}

// watchSetFor asks the project's own service which directories to observe, for
// the same reason the daemon does: the service owns the exclusion policy.
func watchSetFor(ctx context.Context, reference string) ([]string, error) {
	registry, err := projectregistry.Load()
	if err != nil {
		return nil, err
	}
	project, err := registry.Resolve(reference)
	if err != nil {
		return nil, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, codeintel.DefaultWatchSetTimeout)
	defer cancel()

	status, err := newStackManager().Status(probeCtx, project)
	if err != nil {
		return nil, err
	}
	if status.ServiceAddress == "" {
		return nil, fmt.Errorf(
			"project %s is not running a code-intelligence service, so there is nothing to say which directories belong to it; start it with lctk project start",
			project.ID)
	}

	set, err := codeintel.New(status.ServiceAddress).WatchSet(probeCtx)
	if err != nil {
		return nil, err
	}
	if set.Truncated {
		fmt.Fprintf(os.Stderr,
			"lctk: the project has more than %d directories; only the first %d are being watched\n",
			set.Limit, len(set.Directories))
	}
	return set.Directories, nil
}

type settingsView struct {
	Path     string                `json:"path"`
	Present  bool                  `json:"present"`
	Settings hostsettings.Settings `json:"settings"`
}

// runSettingsShow prints the machine-wide policy in force.
//
// It exists because a default nobody can see is a default nobody can change. The
// file path is printed whether or not the file exists, since the usual reason to
// run this is to find out where to write one.
func runSettingsShow(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("settings show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the settings as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: lctk settings show [--json]")
	}

	path, err := hostsettings.Path()
	if err != nil {
		return err
	}
	settings, err := hostsettings.Load()
	if err != nil {
		return err
	}

	view := settingsView{Path: path, Settings: settings}
	if _, statErr := os.Stat(path); statErr == nil {
		view.Present = true
	}

	if *asJSON {
		return writeJSON(stdout, view)
	}

	state := "absent, shipped defaults in force"
	if view.Present {
		state = "present"
	}
	fmt.Fprintf(stdout, "%s (%s)\n", view.Path, state)
	fmt.Fprintf(stdout, "  watch.debounce_ms:             %d\n", settings.Watch.DebounceMS)
	fmt.Fprintf(stdout, "  watch.max_debounce_ms:         %d\n", settings.Watch.MaxDebounceMS)
	fmt.Fprintf(stdout, "  watch.max_watched_directories: %d\n", settings.Watch.MaxWatchedDirectories)
	fmt.Fprintf(stdout, "  watch.idle_stop_seconds:       %d\n", settings.Watch.IdleStopSeconds)
	return nil
}

func runSettings(args []string, stdout, stderr io.Writer) error {
	const usage = "Usage:\n  lctk settings show [--json]\n"
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("a settings subcommand is required")
	}
	switch args[0] {
	case "show":
		return runSettingsShow(args[1:], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown settings subcommand %q", strings.TrimSpace(args[0]))
	}
}
