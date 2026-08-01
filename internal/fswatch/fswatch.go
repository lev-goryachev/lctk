// Package fswatch turns native filesystem events into normalized,
// project-relative changes.
//
// The watcher runs on the host rather than inside the project container because
// the native APIs are the reliable ones: ReadDirectoryChangesW on Windows and
// kqueue on macOS see a save immediately, while the same save observed through a
// Docker Desktop bind mount is delayed, coalesced, or lost. fsnotify is the
// binding to those APIs; recursion, coalescing, and the honesty rule below are
// LCTK's.
//
// The honesty rule is the important part of this package. A watcher that quietly
// misses an event produces a search index that is confidently wrong, which is
// worse than one that is openly behind. So every condition under which
// observation could be incomplete — a buffer overflow, a directory that could not
// be registered, a subtree larger than the watch budget, a consumer too slow to
// drain — is reported as a Gap rather than absorbed. A gap means "stop trusting
// the event stream and reconcile", and the consumer is expected to do exactly
// that.
package fswatch

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Kind is what happened to a path.
type Kind string

const (
	// Written covers creation and modification. The two are deliberately not
	// distinguished: the consumer reads the file and digests it either way, and a
	// watcher cannot reliably tell a create from a write on every platform.
	Written Kind = "written"
	// Removed covers deletion and the disappearing half of a rename. A rename
	// arrives as Removed for the old path and Written for the new one, which is
	// what both native APIs actually report.
	Removed Kind = "removed"
)

// Reasons a gap is recorded. They are values a consumer can branch on and a
// human can read in a status line.
const (
	// ReasonOverflow is the kernel or the library losing events because they
	// arrived faster than they were drained.
	ReasonOverflow = "watcher_overflow"
	// ReasonCapacity is a project with more directories than the watch budget.
	ReasonCapacity = "watch_capacity_exceeded"
	// ReasonUnwatchable is a directory that exists but could not be registered.
	ReasonUnwatchable = "directory_unwatchable"
	// ReasonBacklog is the consumer failing to keep up with this package.
	ReasonBacklog = "consumer_backlog"
	// ReasonWatcherError is any other failure reported by the native watcher.
	ReasonWatcherError = "watcher_error"
)

// Event is one normalized change.
type Event struct {
	// Path is project-relative and uses forward slashes, so it is directly
	// comparable with the paths the index stores.
	Path string `json:"path"`
	Kind Kind   `json:"kind"`
	// Directory says the path named a directory. It matters for removal: a
	// deleted directory takes every file beneath it with it, and the consumer
	// cannot learn that from the path alone once the directory is gone.
	Directory bool      `json:"directory,omitempty"`
	At        time.Time `json:"at"`
}

// Gap reports that the event stream is incomplete from this moment.
type Gap struct {
	Reason string    `json:"reason"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// alwaysExcluded are directories never registered, whatever the project says.
//
// This list is deliberately identical to the indexer's unconditional exclusions
// rather than a copy of its ignore policy. A project can re-include node_modules
// for indexing, so the host must not hard-code it; no project can re-include
// version-control metadata, so the host can. Keeping the host list to exactly the
// rules that cannot be overridden is what stops the two from drifting.
var alwaysExcluded = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {},
}

// Options configures a watcher.
type Options struct {
	// Root is the absolute host path of the project.
	Root string
	// Directories are project-relative directories to observe, with "." meaning
	// the root. They come from the project service, which owns the exclusion
	// policy.
	Directories []string
	// MaxDirectories bounds how many native watches are registered. Past the
	// bound the watcher records a capacity gap and observes a prefix, because a
	// partial event stream plus an explicit "this is partial" is more useful than
	// silence, and far more useful than exhausting the process's handles.
	MaxDirectories int
	// Buffer is the depth of the delivered event channel.
	Buffer int
	Now    func() time.Time
	Logger *slog.Logger
}

// DefaultMaxDirectories is the shipped watch budget.
const DefaultMaxDirectories = 20000

// DefaultBuffer is the shipped depth of the delivered event channel. It absorbs
// an ordinary editor save storm or a branch checkout without dropping anything.
const DefaultBuffer = 4096

// Watcher observes one project.
type Watcher struct {
	root   string
	limit  int
	now    func() time.Time
	logger *slog.Logger

	inner  *fsnotify.Watcher
	events chan Event
	gaps   chan Gap

	closeOnce sync.Once
	done      chan struct{}
	finished  chan struct{}

	mu      sync.Mutex
	watched map[string]struct{}
	// capacityGapped latches the over-budget notice. Every directory past the
	// budget would otherwise report the same thing, and a project 1000 directories
	// over would say it 1000 times. One notice is the whole message: the record is
	// incomplete and reconciliation is required.
	capacityGapped bool
}

// Start begins observing. The returned watcher owns a goroutine until Close.
func Start(options Options) (*Watcher, error) {
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return nil, err
	}
	if options.MaxDirectories <= 0 {
		options.MaxDirectories = DefaultMaxDirectories
	}
	if options.Buffer <= 0 {
		options.Buffer = DefaultBuffer
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}

	inner, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		root:     root,
		limit:    options.MaxDirectories,
		now:      options.Now,
		logger:   options.Logger,
		inner:    inner,
		events:   make(chan Event, options.Buffer),
		gaps:     make(chan Gap, 16),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
		watched:  map[string]struct{}{},
	}

	for _, relative := range options.Directories {
		w.register(relative)
	}
	go w.run()
	return w, nil
}

// Events delivers normalized changes.
func (w *Watcher) Events() <-chan Event { return w.events }

// Gaps delivers notices that the event stream is incomplete.
func (w *Watcher) Gaps() <-chan Gap { return w.gaps }

// Watched reports how many directories are currently registered.
func (w *Watcher) Watched() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.watched)
}

// Close stops observing. It is safe to call more than once.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
		_ = w.inner.Close()
		<-w.finished
	})
	return nil
}

// register adds one project-relative directory, reporting a gap when it exists
// but cannot be observed. A directory that has already disappeared is not a gap:
// its removal is itself an event the consumer will see or reconcile.
func (w *Watcher) register(relative string) {
	relative = normalize(relative)
	if relative == "" {
		return
	}
	if excludedPath(relative) {
		return
	}

	w.mu.Lock()
	if _, already := w.watched[relative]; already {
		w.mu.Unlock()
		return
	}
	if len(w.watched) >= w.limit {
		first := !w.capacityGapped
		w.capacityGapped = true
		w.mu.Unlock()
		if first {
			w.reportGap(ReasonCapacity,
				"the project has more directories than the watch budget of "+strconv.Itoa(w.limit))
		}
		return
	}
	w.mu.Unlock()

	absolute := w.root
	if relative != "." {
		absolute = filepath.Join(w.root, filepath.FromSlash(relative))
	}
	if err := w.inner.Add(absolute); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		w.reportGap(ReasonUnwatchable, relative+": "+err.Error())
		return
	}

	w.mu.Lock()
	w.watched[relative] = struct{}{}
	w.mu.Unlock()
}

func (w *Watcher) unregister(relative string) {
	w.mu.Lock()
	_, known := w.watched[relative]
	delete(w.watched, relative)
	w.mu.Unlock()
	if known {
		absolute := w.root
		if relative != "." {
			absolute = filepath.Join(w.root, filepath.FromSlash(relative))
		}
		_ = w.inner.Remove(absolute)
	}
}

func (w *Watcher) isWatched(relative string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.watched[relative]
	return ok
}

func (w *Watcher) run() {
	defer close(w.finished)
	defer close(w.events)
	defer close(w.gaps)

	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.inner.Events:
			if !ok {
				return
			}
			w.translate(event)
		case err, ok := <-w.inner.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				w.reportGap(ReasonOverflow, err.Error())
				continue
			}
			w.reportGap(ReasonWatcherError, err.Error())
		}
	}
}

// translate converts one native event into the normalized vocabulary.
func (w *Watcher) translate(event fsnotify.Event) {
	relative, ok := w.relative(event.Name)
	if !ok {
		return
	}
	if excludedPath(relative) {
		return
	}

	switch {
	case event.Has(fsnotify.Remove), event.Has(fsnotify.Rename):
		// The path is already gone, so the filesystem cannot say what it was.
		// The watch registry can: every directory the project indexes is
		// registered, so a removed path we were watching was a directory.
		directory := w.isWatched(relative)
		if directory {
			w.unregister(relative)
			w.dropSubtree(relative)
		}
		w.emit(Event{Path: relative, Kind: Removed, Directory: directory, At: w.now()})

	case event.Has(fsnotify.Create):
		info, err := os.Lstat(filepath.Join(w.root, filepath.FromSlash(relative)))
		switch {
		case err != nil:
			// Created and gone again before this read. Report it as a write and
			// let the consumer discover the file is absent; the alternative,
			// silence, would lose a real create-then-rename sequence.
			w.emit(Event{Path: relative, Kind: Written, At: w.now()})
		case info.IsDir():
			// Registered before it is reported, not after. Between the two there
			// is a window in which the directory is known to the consumer but not
			// to the watch registry, and anything happening inside it during that
			// window is invisible. Worse, its own removal would then be reported
			// as a file removal, and the consumer would leave every file that was
			// inside it in the index.
			w.register(relative)
			w.emit(Event{Path: relative, Kind: Written, Directory: true, At: w.now()})
			w.adopt(relative)
		case info.Mode()&os.ModeSymlink != 0:
			// The indexer skips symbolic links, so reporting one would only
			// produce work that is guaranteed to be discarded.
		default:
			w.emit(Event{Path: relative, Kind: Written, At: w.now()})
		}

	case event.Has(fsnotify.Write):
		// Windows reports a write against the containing directory as well as the
		// file. A directory has no content to index, so reporting it would inflate
		// every save into two changes and make each one trigger an index update
		// for a path the indexer is certain to discard.
		if w.isWatched(relative) {
			return
		}
		w.emit(Event{Path: relative, Kind: Written, At: w.now()})

		// A chmod is deliberately not an event. Content changes surface as Write
		// or Create on both target platforms, while tools that rewrite modes in
		// bulk would otherwise produce a storm of changes that digest to exactly
		// what is already indexed.
	}
}

// adopt registers a newly created directory and reports what is already inside
// it.
//
// The walk is not defensive tidiness. Between a directory being created and a
// watch being placed on it there is a real window, and an editor or a build tool
// writing a whole tree at once will fill it during that window. Without the walk
// those files are invisible until something else happens to touch them.
func (w *Watcher) adopt(relative string) {
	base := filepath.Join(w.root, filepath.FromSlash(relative))
	_ = filepath.WalkDir(base, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		child, ok := w.relative(name)
		if !ok || excludedPath(child) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			w.register(child)
			if child != relative {
				w.emit(Event{Path: child, Kind: Written, Directory: true, At: w.now()})
			}
			return nil
		}
		if entry.Type().IsRegular() {
			w.emit(Event{Path: child, Kind: Written, At: w.now()})
		}
		return nil
	})
}

// dropSubtree forgets watches beneath a removed directory. The native watch is
// already invalid; keeping the entry would make a later re-creation look like a
// directory that is still observed.
func (w *Watcher) dropSubtree(relative string) {
	prefix := relative + "/"
	w.mu.Lock()
	var stale []string
	for name := range w.watched {
		if strings.HasPrefix(name, prefix) {
			stale = append(stale, name)
		}
	}
	for _, name := range stale {
		delete(w.watched, name)
	}
	w.mu.Unlock()
}

// emit delivers an event, or records a gap rather than blocking.
//
// Blocking here would stall the goroutine draining the native watcher, which is
// precisely how the kernel buffer overflows. Dropping with an explicit gap keeps
// the failure visible and bounded.
func (w *Watcher) emit(event Event) {
	select {
	case w.events <- event:
	case <-w.done:
	default:
		w.reportGap(ReasonBacklog, "the change consumer did not keep up")
	}
}

func (w *Watcher) reportGap(reason, detail string) {
	gap := Gap{Reason: reason, Detail: detail, At: w.now()}
	select {
	case w.gaps <- gap:
	case <-w.done:
	default:
		// The gap channel is already holding notices the consumer has not read.
		// One more would say nothing new: a gap is a latch, not a count.
		w.logger.Debug("gap notice dropped", slog.String("reason", reason))
	}
}

// relative converts a native event path into a project-relative one, refusing
// anything that does not sit under the root.
func (w *Watcher) relative(name string) (string, bool) {
	rel, err := filepath.Rel(w.root, name)
	if err != nil {
		return "", false
	}
	slashed := filepath.ToSlash(rel)
	if slashed == "." || slashed == ".." || strings.HasPrefix(slashed, "../") {
		return "", false
	}
	return slashed, true
}

func normalize(relative string) string {
	trimmed := filepath.ToSlash(strings.TrimSpace(relative))
	if trimmed == "" {
		return "."
	}
	return trimmed
}

// excludedPath reports whether any segment names a directory that is excluded
// unconditionally.
func excludedPath(relative string) bool {
	if relative == "." {
		return false
	}
	for _, segment := range strings.Split(relative, "/") {
		if _, excluded := alwaysExcluded[segment]; excluded {
			return true
		}
	}
	return false
}
