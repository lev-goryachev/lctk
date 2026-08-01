package watchsupervisor

import (
	"log/slog"
	"sync"
	"time"

	"github.com/lev-goryachev/lctk/internal/changejournal"
	"github.com/lev-goryachev/lctk/internal/fswatch"
	"github.com/lev-goryachev/lctk/internal/hostsettings"
)

// worker observes one project and keeps its journal.
type worker struct {
	projectID string
	journal   *changejournal.Journal
	watcher   *fswatch.Watcher
	watch     hostsettings.Watch
	logger    *slog.Logger
	now       func() time.Time

	stopOnce sync.Once
	done     chan struct{}
	finished chan struct{}

	mu        sync.Mutex
	lastUse   time.Time
	settledAt time.Time
}

// run applies the debounce policy to the event stream.
//
// Two timers, not one. The debounce timer restarts on every change, which is what
// collapses a save-format-save burst into a single update. The ceiling timer does
// not restart, which is what stops a project somebody is typing in continuously
// from deferring its update forever.
func (w *worker) run() {
	defer close(w.finished)

	// Persist before waiting for anything. The journal opened with a gap covering
	// the period nobody was watching, and that is a fact about the project whether
	// or not a change ever follows it. Without this, a quiet project has no
	// document on disk and a status command reports "never observed" about a
	// project that is being observed right now.
	w.settle()

	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	defer debounce.Stop()

	ceiling := time.NewTimer(time.Hour)
	ceiling.Stop()
	defer ceiling.Stop()
	ceilingArmed := false

	arm := func() {
		debounce.Reset(w.watch.Debounce())
		if !ceilingArmed {
			ceiling.Reset(w.watch.SettleCeiling())
			ceilingArmed = true
		}
	}

	for {
		select {
		case <-w.done:
			w.settle()
			return

		case event, ok := <-w.watcher.Events():
			if !ok {
				w.settle()
				return
			}
			w.journal.Record(event)
			w.touch(event.At)
			arm()

		case gap, ok := <-w.watcher.Gaps():
			if !ok {
				w.settle()
				return
			}
			w.journal.MarkGap(gap.Reason, gap.Detail, gap.At)
			w.logger.Warn("change record is incomplete",
				slog.String("reason", gap.Reason),
				slog.String("detail", gap.Detail))
			// A gap is settled on the same timer as a change rather than at once.
			// It usually arrives in the middle of the burst that caused it, and
			// persisting once at the end of the burst is both cheaper and just as
			// timely for a consumer that only acts when the batch settles.
			arm()

		case <-debounce.C:
			ceiling.Stop()
			ceilingArmed = false
			w.settle()

		case <-ceiling.C:
			debounce.Stop()
			ceilingArmed = false
			w.settle()
		}
	}
}

// settle persists the journal and records when the batch became quiet.
func (w *worker) settle() {
	if err := w.journal.Save(); err != nil {
		w.logger.Warn("change journal could not be written", slog.String("error", err.Error()))
		return
	}
	w.mu.Lock()
	w.settledAt = w.now()
	w.mu.Unlock()
}

// touch records recent activity, which is what keeps a busy project's watcher
// from being reaped as idle.
func (w *worker) touch(at time.Time) {
	if at.IsZero() {
		at = w.now()
	}
	w.mu.Lock()
	if at.After(w.lastUse) {
		w.lastUse = at
	}
	w.mu.Unlock()
}

func (w *worker) idleSince(now time.Time) bool {
	w.mu.Lock()
	last := w.lastUse
	w.mu.Unlock()
	return now.Sub(last) > w.watch.IdleStop()
}

// stop releases the watcher and records that observation has ended.
//
// The gap is written after the watcher is closed, so the record says the project
// stopped being observed from that moment rather than from some point before it.
func (w *worker) stop(reason, detail string) {
	w.stopOnce.Do(func() {
		close(w.done)
		<-w.finished
		_ = w.watcher.Close()

		w.journal.MarkGap(reason, detail, w.now())
		if err := w.journal.Save(); err != nil {
			w.logger.Warn("change journal could not be written on shutdown",
				slog.String("error", err.Error()))
		}
		w.logger.Info("released project watcher", slog.String("detail", detail))
	})
}

func (w *worker) view() View {
	snapshot := w.journal.Snapshot()

	w.mu.Lock()
	settledAt := w.settledAt
	w.mu.Unlock()

	return View{
		ProjectID:       w.projectID,
		Watching:        true,
		Directories:     w.watcher.Watched(),
		Pending:         len(snapshot.Pending),
		Sequence:        snapshot.Sequence,
		Checkpoint:      snapshot.Checkpoint,
		Gap:             snapshot.Gap,
		LastEventAt:     snapshot.LastEventAt,
		SettledAt:       settledAt,
		DebounceSeconds: w.watch.Debounce().Seconds(),
	}
}
