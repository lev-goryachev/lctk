package searchindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/index"
)

// SchemaVersion is the on-disk layout version of a published generation. A
// generation written by a different version is rebuilt rather than read, because
// the shard format is Zoekt's and LCTK does not attempt to migrate it.
const SchemaVersion = 1

const (
	generationsDir = "generations"
	currentLink    = "current"
	stateName      = "state.json"
	stagingPrefix  = ".staging-"
)

// Limits bound the work the index will do. They are values rather than constants
// so a test can drive the same policy at small scale.
type Limits struct {
	// MaxFileBytes skips files larger than this. A generated bundle or a
	// checked-in binary costs index space and answers nothing useful.
	MaxFileBytes int64
	// MaxDeltaGenerations is how many delta builds may accumulate before the next
	// update is escalated to a full rebuild. Zoekt resolves a search across every
	// shard, so unbounded deltas degrade every query, not just the next update.
	MaxDeltaGenerations int
	// KeepGenerations is how many published generations are retained. More than
	// one is kept so a search already in flight is not reading a deleted
	// directory when a new generation is published.
	KeepGenerations int
}

// DefaultLimits are the shipped policy.
var DefaultLimits = Limits{
	MaxFileBytes:        1 << 20,
	MaxDeltaGenerations: 32,
	KeepGenerations:     2,
}

func (l Limits) withDefaults() Limits {
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = DefaultLimits.MaxFileBytes
	}
	if l.MaxDeltaGenerations <= 0 {
		l.MaxDeltaGenerations = DefaultLimits.MaxDeltaGenerations
	}
	if l.KeepGenerations <= 0 {
		l.KeepGenerations = DefaultLimits.KeepGenerations
	}
	return l
}

// State is the metadata published alongside one generation's shards.
type State struct {
	SchemaVersion int `json:"schema_version"`
	// Generation increases on every publication and is reported to the caller as
	// provenance, so a client can tell whether two answers describe the same
	// index.
	Generation uint64 `json:"generation"`
	// DeltaDepth counts delta builds since the last full build.
	DeltaDepth int `json:"delta_depth"`
	// Files maps a project-relative path to a content digest. It is LCTK's own
	// inventory, which is what allows an uncommitted saved file to be indexed.
	Files     map[string]string `json:"files"`
	FileCount int               `json:"file_count"`
	// SkippedBig counts files above the size limit.
	SkippedBig int `json:"skipped_too_large"`
	// SkippedIgnored counts entries the project's own ignore rules excluded, so
	// the effect of those rules is reportable rather than invisible.
	SkippedIgnored int       `json:"skipped_ignored"`
	BuiltAt        time.Time `json:"built_at"`
	FullBuild      bool      `json:"full_build"`
}

// Change is one file event to apply.
type Change struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted,omitempty"`
}

// Store owns the persistent index for a single project.
//
// One project, one workspace, one state root: the type has no way to express a
// second project, which is how the isolation requirement in docs/security.md is
// met here rather than by checking an identifier on every call.
type Store struct {
	// Workspace is the read-only project source mount.
	Workspace string
	// Root is the directory inside the project's persistent volume that holds
	// every generation.
	Root      string
	ProjectID string
	Limits    Limits

	mu       sync.RWMutex
	searcher zoekt.Searcher
	openGen  uint64
}

// New returns a store with defaults applied.
func New(workspace, root, projectID string, limits Limits) *Store {
	return &Store{
		Workspace: workspace,
		Root:      root,
		ProjectID: projectID,
		Limits:    limits.withDefaults(),
	}
}

func (s *Store) generationsPath() string { return filepath.Join(s.Root, generationsDir) }
func (s *Store) currentPath() string     { return filepath.Join(s.Root, currentLink) }

func generationName(n uint64) string { return fmt.Sprintf("%06d", n) }

// State returns the published state, or a typed not-ready error when no
// generation exists yet.
func (s *Store) State() (State, error) {
	_, state, err := s.resolveCurrent()
	return state, err
}

// resolveCurrent reads the published generation directory and its state.
func (s *Store) resolveCurrent() (string, State, error) {
	dir, err := filepath.EvalSymlinks(s.currentPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", State{}, fail(CodeIndexNotReady,
				"The project index has not been built yet.", true, err)
		}
		return "", State{}, fail(CodeIndexCorrupt,
			"The published index generation cannot be resolved.", false, err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, stateName))
	if err != nil {
		return "", State{}, fail(CodeIndexCorrupt,
			"The published index generation has no readable state.", false, err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return "", State{}, fail(CodeIndexCorrupt,
			"The published index state is not valid JSON.", false, err)
	}
	if state.SchemaVersion != SchemaVersion {
		return "", State{}, fail(CodeIndexCorrupt,
			fmt.Sprintf("The published index uses schema version %d, this build understands %d.",
				state.SchemaVersion, SchemaVersion), false, nil)
	}
	if state.Files == nil {
		state.Files = map[string]string{}
	}
	return dir, state, nil
}

// Rebuild indexes the whole workspace into a new generation and publishes it.
func (s *Store) Rebuild(ctx context.Context) (State, error) {
	inventory, err := s.inventory(ctx)
	if err != nil {
		return State{}, err
	}

	previous := uint64(0)
	if _, state, err := s.resolveCurrent(); err == nil {
		previous = state.Generation
	}

	return s.build(ctx, buildPlan{
		generation: previous + 1,
		full:       true,
		files:      inventory.files,
		skippedBig: inventory.skippedBig,
		ignored:    inventory.skippedIgnored,
		add:        sortedKeys(inventory.files),
	})
}

// Update applies a batch of changes as a delta generation.
//
// It escalates to a full rebuild when the delta depth would exceed the policy,
// so a long-running project cannot silently degrade into a pile of shards.
func (s *Store) Update(ctx context.Context, changes []Change) (State, error) {
	dir, state, err := s.resolveCurrent()
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) && typed.Code == CodeIndexNotReady {
			return s.Rebuild(ctx)
		}
		return State{}, err
	}
	if len(changes) == 0 {
		return state, nil
	}
	if state.DeltaDepth+1 > s.Limits.MaxDeltaGenerations {
		return s.Rebuild(ctx)
	}

	files := make(map[string]string, len(state.Files))
	for name, digest := range state.Files {
		files[name] = digest
	}

	var (
		touched = make(map[string]struct{}, len(changes))
		add     []string
	)
	for _, change := range changes {
		name, err := normalizeRelative(change.Path)
		if err != nil {
			return State{}, fail(CodeInvalidPattern, err.Error(), false, nil)
		}
		if _, seen := touched[name]; seen {
			continue
		}
		touched[name] = struct{}{}

		if change.Deleted {
			delete(files, name)
			continue
		}
		// A targeted update must agree with what a full build would do, or an
		// ignored file added here would vanish again at the next rebuild.
		if !s.eligible(name) {
			delete(files, name)
			continue
		}
		digest, size, err := s.digestFile(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// A change that names a file which is now gone is a delete. The
			// watcher cannot promise otherwise, and treating it as an error would
			// make a normal race fatal.
			delete(files, name)
			continue
		case err != nil:
			return State{}, internal("read "+name, err)
		case size > s.Limits.MaxFileBytes:
			delete(files, name)
			continue
		}
		files[name] = digest
		add = append(add, name)
	}
	sort.Strings(add)

	return s.build(ctx, buildPlan{
		generation: state.Generation + 1,
		full:       false,
		previous:   dir,
		deltaDepth: state.DeltaDepth + 1,
		files:      files,
		skippedBig: state.SkippedBig,
		ignored:    state.SkippedIgnored,
		add:        add,
		tombstone:  sortedSet(touched),
	})
}

// Reconcile compares the workspace with the published inventory and applies the
// difference. It is how the index catches up after the service was not running.
func (s *Store) Reconcile(ctx context.Context) (State, []Change, error) {
	_, state, err := s.resolveCurrent()
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) && typed.Code == CodeIndexNotReady {
			built, buildErr := s.Rebuild(ctx)
			return built, nil, buildErr
		}
		return State{}, nil, err
	}

	inventory, err := s.inventory(ctx)
	if err != nil {
		return State{}, nil, err
	}

	var changes []Change
	for name, digest := range inventory.files {
		if state.Files[name] != digest {
			changes = append(changes, Change{Path: name})
		}
	}
	for name := range state.Files {
		if _, present := inventory.files[name]; !present {
			changes = append(changes, Change{Path: name, Deleted: true})
		}
	}
	sort.Slice(changes, func(a, b int) bool { return changes[a].Path < changes[b].Path })

	if len(changes) == 0 {
		return state, nil, nil
	}
	updated, err := s.Update(ctx, changes)
	return updated, changes, err
}

type buildPlan struct {
	generation uint64
	full       bool
	previous   string
	deltaDepth int
	files      map[string]string
	skippedBig int
	ignored    int
	add        []string
	tombstone  []string
}

// build writes one generation into a staging directory and publishes it
// atomically.
//
// Staging is not a nicety. A search reads the directory `current` points at, so
// building in place would expose a half-written index to a live query. Building
// aside and moving one symlink means a reader sees either the whole previous
// generation or the whole new one.
func (s *Store) build(ctx context.Context, plan buildPlan) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(s.generationsPath(), 0o755); err != nil {
		return State{}, internal("create the generations directory", err)
	}

	staging, err := os.MkdirTemp(s.Root, stagingPrefix)
	if err != nil {
		return State{}, internal("create a staging directory", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()

	// A delta build needs the previous shards beside it, because Zoekt resolves a
	// search across the whole directory. They are hard-linked rather than copied:
	// the shards are immutable once written, so a link is both correct and free.
	if !plan.full && plan.previous != "" {
		if err := linkShards(plan.previous, staging); err != nil {
			return State{}, internal("carry the previous shards into the new generation", err)
		}
	}

	builder, err := index.NewBuilder(s.options(staging, !plan.full))
	if err != nil {
		return State{}, internal("create the index builder", err)
	}
	for _, name := range plan.tombstone {
		builder.MarkFileAsChangedOrRemoved(name)
	}
	for _, name := range plan.add {
		if err := ctx.Err(); err != nil {
			return State{}, err
		}
		content, err := os.ReadFile(filepath.Join(s.Workspace, filepath.FromSlash(name)))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return State{}, internal("read "+name, err)
		}
		if err := builder.AddFile(name, content); err != nil {
			return State{}, internal("index "+name, err)
		}
	}
	if err := builder.Finish(); err != nil {
		return State{}, internal("finish the index build", err)
	}

	state := State{
		SchemaVersion:  SchemaVersion,
		Generation:     plan.generation,
		DeltaDepth:     plan.deltaDepth,
		Files:          plan.files,
		FileCount:      len(plan.files),
		SkippedBig:     plan.skippedBig,
		SkippedIgnored: plan.ignored,
		BuiltAt:        time.Now().UTC().Truncate(time.Second),
		FullBuild:      plan.full,
	}
	if err := writeState(filepath.Join(staging, stateName), state); err != nil {
		return State{}, err
	}

	target := filepath.Join(s.generationsPath(), generationName(plan.generation))
	if err := os.RemoveAll(target); err != nil {
		return State{}, internal("clear the target generation directory", err)
	}
	if err := os.Rename(staging, target); err != nil {
		return State{}, internal("move the staged generation into place", err)
	}
	committed = true

	if err := s.publish(target); err != nil {
		return State{}, err
	}
	s.invalidate()
	s.prune(plan.generation)
	return state, nil
}

// publish points `current` at a generation directory in one atomic step.
func (s *Store) publish(target string) error {
	link := s.currentPath()
	temporary := link + ".tmp"
	if err := os.RemoveAll(temporary); err != nil {
		return internal("clear the temporary publication link", err)
	}
	// The link is relative so the whole state volume stays movable.
	relative, err := filepath.Rel(s.Root, target)
	if err != nil {
		relative = target
	}
	if err := os.Symlink(relative, temporary); err != nil {
		return internal("create the publication link", err)
	}
	if err := os.Rename(temporary, link); err != nil {
		_ = os.Remove(temporary)
		return internal("publish the new generation", err)
	}
	return nil
}

// prune removes generations older than the retention policy. Failure to prune is
// not failure to publish, so it is deliberately not returned: the index is
// correct either way, and refusing a successful build over stale disk would be
// the wrong trade.
func (s *Store) prune(current uint64) {
	entries, err := os.ReadDir(s.generationsPath())
	if err != nil {
		return
	}
	keep := uint64(s.Limits.KeepGenerations)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		number, err := strconv.ParseUint(strings.TrimLeft(entry.Name(), "0"), 10, 64)
		if err != nil || number == 0 {
			continue
		}
		if current >= keep && number <= current-keep {
			_ = os.RemoveAll(filepath.Join(s.generationsPath(), entry.Name()))
		}
	}
}

func (s *Store) options(dir string, delta bool) index.Options {
	options := index.Options{
		IndexDir: dir,
		RepositoryDescription: zoekt.Repository{
			ID:   1,
			Name: s.ProjectID,
		},
		// ctags would add symbol metadata and a native dependency. Neither is part
		// of the exact-search contract, so it stays off.
		DisableCTags: true,
		IsDelta:      delta,
	}
	options.SetDefaults()
	return options
}

func linkShards(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.Contains(name, ".zoekt") {
			continue
		}
		source := filepath.Join(from, name)
		if err := os.Link(source, filepath.Join(to, name)); err != nil {
			return err
		}
	}
	return nil
}

func writeState(path string, state State) error {
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return internal("encode the index state", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return internal("write the index state", err)
	}
	return nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSet(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
