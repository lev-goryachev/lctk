// Package changejournal records what the host watcher observed about one
// project, and how far a consumer has caught up.
//
// The journal exists so that a running daemon can update an index by applying
// three changes instead of re-walking and re-digesting the whole project. That is
// its only purpose, and it means the journal is worth exactly as much as its
// honesty: a pending list that silently omits a change would make the index
// confidently stale. So the journal is either complete since its checkpoint, or
// it says so with a Gap.
//
// A gap is a latch, not a counter. Once set it stays set until a consumer
// reconciles the filesystem with the index and says so, because a second reason
// to distrust the stream adds nothing to the first.
//
// The journal deliberately claims nothing about periods when it was not loaded.
// Loading one always records a gap: files can change while the daemon is not
// running, and no amount of persisted state can rule that out. What persistence
// buys is that a continuously running daemon never needs to reconcile, and that
// work observed but not yet applied is not lost when the process ends.
package changejournal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lev-goryachev/lctk/internal/fswatch"
	"github.com/lev-goryachev/lctk/internal/lctkhome"
)

// SchemaVersion is the on-disk layout version.
const SchemaVersion = 1

// DirName is the LCTK home subdirectory holding one document per project.
const DirName = "journals"

// DefaultMaxPending bounds how many distinct paths the journal will track before
// declaring the change bulk.
//
// The bound is not about memory. Past some size, applying changes one by one
// stops being cheaper than walking the project, and a batch that large is
// usually a branch checkout or a code generator rather than editing. Turning it
// into a gap routes it to the path that handles that properly.
const DefaultMaxPending = 10000

// Reasons this package records in addition to the watcher's own.
const (
	// ReasonStarted marks the unobserved period before the journal was loaded.
	ReasonStarted = "observation_started"
	// ReasonUnreadable marks a journal that could not be read and was reset.
	ReasonUnreadable = "journal_unreadable"
	// ReasonBulk marks a change too large to be worth applying incrementally.
	ReasonBulk = "bulk_change"
)

// Entry is one observed change. There is at most one per path.
type Entry struct {
	Seq       uint64       `json:"seq"`
	Path      string       `json:"path"`
	Kind      fswatch.Kind `json:"kind"`
	Directory bool         `json:"directory,omitempty"`
	At        time.Time    `json:"at"`
}

// Gap says the record is incomplete from a point in time.
type Gap struct {
	Reason string    `json:"reason"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// Snapshot is the whole journal, and also its on-disk shape.
type Snapshot struct {
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	// Sequence is the last number assigned to an observation.
	Sequence uint64 `json:"sequence"`
	// Checkpoint is the last sequence a consumer confirmed it applied.
	Checkpoint uint64 `json:"checkpoint"`
	// Generation is the index generation that existed at the checkpoint, so a
	// consumer can tell whether the index in front of it is the one the
	// checkpoint describes.
	Generation uint64 `json:"generation"`
	// GapSeq counts how many times the record has been broken. It exists so a
	// consumer can tell "the gap I set out to close" from "a new gap that opened
	// while I was closing it".
	GapSeq uint64 `json:"gap_seq"`
	Gap    *Gap   `json:"gap,omitempty"`
	// Pending is ordered by sequence, oldest first.
	Pending []Entry `json:"pending,omitempty"`
	// LastEventAt is when the most recent observation arrived, which is what a
	// status line needs in order to say how long a project has been quiet.
	LastEventAt time.Time `json:"last_event_at,omitzero"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Mark is the point a consumer set out to catch up to.
type Mark struct {
	Sequence uint64
	GapSeq   uint64
}

// Mark returns the point this snapshot describes.
//
// A consumer should take it from the snapshot it is about to act on rather than
// asking the journal separately. The snapshot is consistent; two calls are not,
// and the difference is a change observed between them that would be committed
// without being applied.
func (s Snapshot) Mark() Mark {
	return Mark{Sequence: s.Sequence, GapSeq: s.GapSeq}
}

// Journal is the in-memory record for one project, persisted on demand.
type Journal struct {
	path       string
	maxPending int
	now        func() time.Time

	mu   sync.Mutex
	meta Snapshot
	// pending is keyed by path because deduplication is the point: a file saved
	// fifty times is one change to apply, and a list would make every repeat a
	// linear scan.
	pending map[string]Entry
	dirty   bool
}

// Options configures a journal.
type Options struct {
	// Path is the document to read and write. Empty means the per-user default
	// for the project.
	Path       string
	MaxPending int
	Now        func() time.Time
}

// PathFor returns the journal document for a project without creating anything.
func PathFor(projectID string) (string, error) {
	dir, err := lctkhome.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DirName, projectID+".json"), nil
}

// Open loads the journal for a project, creating an empty one when absent.
//
// Loading always records a gap. The journal describes what the watcher saw, and
// between the previous process ending and this one starting the watcher saw
// nothing, so nothing on disk can establish that the project is unchanged.
func Open(projectID string, options Options) (*Journal, error) {
	path := options.Path
	if path == "" {
		resolved, err := PathFor(projectID)
		if err != nil {
			return nil, err
		}
		path = resolved
	}
	if options.MaxPending <= 0 {
		options.MaxPending = DefaultMaxPending
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	j := &Journal{
		path:       path,
		maxPending: options.MaxPending,
		now:        options.Now,
		meta:       Snapshot{SchemaVersion: SchemaVersion, ProjectID: projectID},
		pending:    map[string]Entry{},
	}

	reason, detail := ReasonStarted, "the daemon was not observing this project until now"
	loaded, err := read(path)
	switch {
	case err != nil:
		// A journal is disposable in a way the registry is not: discarding it
		// costs one reconciliation, while discarding a registration would detach a
		// project from its data. So an unreadable journal is reset rather than
		// treated as a fatal condition, and the gap makes the reset visible.
		reason, detail = ReasonUnreadable, err.Error()
	case loaded == nil:
	case loaded.SchemaVersion != SchemaVersion || loaded.ProjectID != projectID:
		reason = ReasonUnreadable
		detail = fmt.Sprintf("the stored journal is schema %d for project %q",
			loaded.SchemaVersion, loaded.ProjectID)
	default:
		j.meta = *loaded
		j.meta.Pending = nil
		for _, entry := range loaded.Pending {
			j.pending[entry.Path] = entry
		}
	}

	j.markGap(reason, detail, j.now())
	return j, nil
}

// Record adds observations, keeping one entry per path.
//
// A repeated save of the same file is one pending change, not fifty. The entry is
// re-sequenced rather than updated in place, so a consumer that committed up to
// an earlier sequence still sees the newer observation.
func (j *Journal) Record(events ...fswatch.Event) {
	if len(events) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	for _, event := range events {
		path := strings.TrimSpace(event.Path)
		if path == "" || path == "." {
			continue
		}
		j.meta.Sequence++
		j.pending[path] = Entry{
			Seq:       j.meta.Sequence,
			Path:      path,
			Kind:      event.Kind,
			Directory: event.Directory,
			At:        event.At,
		}
		if event.At.After(j.meta.LastEventAt) {
			j.meta.LastEventAt = event.At
		}
	}
	j.dirty = true

	if len(j.pending) > j.maxPending {
		// Past the bound the list stops being the cheaper answer. It is dropped
		// rather than trimmed, because a partial list read as complete is the one
		// outcome the journal exists to prevent.
		j.pending = map[string]Entry{}
		j.markGapLocked(ReasonBulk,
			fmt.Sprintf("more than %d paths changed without being applied", j.maxPending), j.now())
	}
}

// MarkGap records that the event stream is no longer trustworthy.
func (j *Journal) MarkGap(reason, detail string, at time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.markGapLocked(reason, detail, at)
}

func (j *Journal) markGap(reason, detail string, at time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.markGapLocked(reason, detail, at)
}

func (j *Journal) markGapLocked(reason, detail string, at time.Time) {
	j.meta.GapSeq++
	j.dirty = true
	if j.meta.Gap != nil {
		// The first reason is kept. It names the earliest moment the record
		// stopped being complete, which is the only part a consumer acts on.
		return
	}
	if at.IsZero() {
		at = j.now()
	}
	j.meta.Gap = &Gap{Reason: reason, Detail: detail, At: at}
}

// Mark captures the point a consumer is about to catch up to.
func (j *Journal) Mark() Mark {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Mark{Sequence: j.meta.Sequence, GapSeq: j.meta.GapSeq}
}

// Pending returns the outstanding changes, oldest first.
func (j *Journal) Pending() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.pendingLocked()
}

// Commit records that every change up to a sequence has been applied to an index
// generation. It does not clear a gap: applying a known-incomplete list does not
// make it complete.
func (j *Journal) Commit(through, generation uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.advanceLocked(through, generation)
}

// Reconciled records that the consumer compared the filesystem with the index
// directly, which subsumes everything observed up to the mark.
//
// The gap is cleared only when it is the same gap the caller set out to close. A
// gap that opened while the reconciliation was running describes changes the
// reconciliation may have missed, and clearing it would lose them.
func (j *Journal) Reconciled(mark Mark, generation uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.advanceLocked(mark.Sequence, generation)
	if j.meta.GapSeq == mark.GapSeq {
		j.meta.Gap = nil
	}
}

func (j *Journal) advanceLocked(through, generation uint64) {
	for path, entry := range j.pending {
		if entry.Seq <= through {
			delete(j.pending, path)
		}
	}
	if through > j.meta.Checkpoint {
		j.meta.Checkpoint = through
	}
	j.meta.Generation = generation
	j.dirty = true
}

// Snapshot returns a copy of the current record.
func (j *Journal) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

func (j *Journal) snapshotLocked() Snapshot {
	copied := j.meta
	copied.Pending = j.pendingLocked()
	if j.meta.Gap != nil {
		gap := *j.meta.Gap
		copied.Gap = &gap
	}
	return copied
}

func (j *Journal) pendingLocked() []Entry {
	entries := make([]Entry, 0, len(j.pending))
	for _, entry := range j.pending {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Seq < entries[b].Seq })
	return entries
}

// Path is the document this journal reads and writes.
func (j *Journal) Path() string { return j.path }

// Save writes the journal atomically, doing nothing when unchanged.
func (j *Journal) Save() error {
	j.mu.Lock()
	if !j.dirty {
		j.mu.Unlock()
		return nil
	}
	j.meta.UpdatedAt = j.now().UTC().Truncate(time.Second)
	j.meta.SchemaVersion = SchemaVersion
	document := j.snapshotLocked()
	j.mu.Unlock()

	if err := write(j.path, document); err != nil {
		return err
	}

	j.mu.Lock()
	j.dirty = false
	j.mu.Unlock()
	return nil
}

func read(path string) (*Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read change journal %q: %w", path, err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("change journal %q is not valid JSON: %w", path, err)
	}
	return &snapshot, nil
}

// write replaces the document atomically, so an interrupted write cannot leave a
// half-parsed journal that would be read as a shorter pending list.
func write(path string, document Snapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %q: %w", dir, err)
	}

	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode change journal: %w", err)
	}
	encoded = append(encoded, '\n')

	temp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create a temporary change journal in %q: %w", dir, err)
	}
	name := temp.Name()
	defer os.Remove(name)

	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return fmt.Errorf("write the temporary change journal: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close the temporary change journal: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("restrict the temporary change journal: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace change journal %q: %w", path, err)
	}
	return nil
}
