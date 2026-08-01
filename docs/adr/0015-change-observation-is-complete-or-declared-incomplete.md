# ADR-0015: Change observation is complete or declared incomplete

- Status: accepted
- Date: 2026-08-01
- Deciders: project maintainers

## Context

[ADR-0005](0005-host-watcher-and-incremental-indexing.md) decided that the host daemon watches registered roots through native APIs and feeds debounced batches to the project indexer. It left the hard parts open, and Slice 2.1 had to settle them before any of it could be trusted:

- a filesystem watcher can lose events, and every platform has its own way of doing so — a full kernel buffer, a directory that appears before a watch can be placed on it, a subtree too large to register;
- the host must know which directories belong to the project, but the exclusion policy lives in the project service, which owns [`.gitignore`, `.lctkignore`, and `.lctkignore.local`](../indexing.md#exclusions);
- native watches are a finite resource — one handle per directory on Windows, one descriptor per path under kqueue — and this repository alone holds 56,077 directories, most of them inside an ignored local cache;
- the debounce window was described as "3–5 seconds under discussion" with no accepted default and no decided configuration scope.

The consequence of getting the first point wrong is specific and severe. A search index that is behind and says so costs a caller one extra step. A search index that is behind and reports itself current causes an agent to act on code that no longer exists, and nothing in the answer hints at it.

## Decision

### The record is complete since its checkpoint, or it carries a gap

The host keeps a per-project change journal. It makes exactly one claim: every change to the project since its checkpoint is in the pending list, or a gap says otherwise. There is no third state and no partial credit.

A gap is recorded whenever observation could have been incomplete, without waiting for evidence that something was actually missed:

- the native watcher reported an overflow or an error;
- a directory in the watch set exists but could not be registered;
- the project has more directories than the watch budget;
- the consumer did not drain events fast enough;
- more paths changed than the journal will track;
- the journal was loaded, which by construction follows a period when nothing was watching;
- the watcher was released because the project stopped or went idle.

A gap is a latch, not a counter. It keeps the reason from the earliest moment the record stopped being complete, because that is the only part a consumer acts on, and it is cleared only by a consumer that reconciled the filesystem with the index and can prove it is closing the gap it set out to close rather than one that opened while it worked.

Loading a journal always records a gap. This is the rule that costs the most and is least negotiable: no amount of persisted state can establish that a project was unchanged while the daemon was not running. What persistence buys is that a *continuously running* daemon never needs to reconcile, and that work observed but not yet applied survives the process ending.

### The project service decides what is watched

The host asks the project's own service for the directories to observe, and watches those. It does not read ignore files itself.

The alternative — a host-side copy of the ignore engine — was rejected because two implementations of "what belongs to this project" drift, and the one that drifts is the watcher: it stops reporting a directory the indexer still cares about, and nothing fails. There is no error, no missing result, just a file that never updates. The single-authority arrangement makes that impossible by construction, at the cost of one HTTP round trip when a watcher starts and of watching nothing for a project whose service is not running — which is correct, since a stopped project has no index to keep current.

Two exclusions are hard-coded on the host anyway: `.git`, `.hg`, and `.svn`. That is safe precisely because they are the rules a project *cannot* override, so no divergence is possible. Everything a project can re-include is left to the service.

### Watching has a budget, and exceeding it is a gap rather than a failure

A project may hold at most a configured number of native watches, 20,000 by default. Past the budget the watcher observes a prefix of the tree and records a capacity gap once.

Refusing to watch at all would be simpler, but it would abandon exactly the large projects where incremental updates matter most. Watching silently past the budget is not an option on either target platform. A partial event stream plus an explicit "this is partial" is strictly more useful than either, because the gap routes the project to reconciliation, which is correct if slow.

### Debounce has a machine default and a project proposal, and the host holds the bounds

The shipped debounce is **3 seconds** after the most recent change, with each new change restarting the wait and a 30-second ceiling on how long continuous editing may defer an update.

The machine owner changes it in `settings.json` in the LCTK home. A repository may *propose* a different window in its manifest, because a repository knows its own editing shape better than a global default does. The host clamps the proposal to between 200 ms and 60 seconds and never accepts it as given.

The floor is not arbitrary. An editor save is commonly a write to a temporary file followed by a rename, and reacting between the two indexes a file that is about to be replaced. The ceiling exists because the point of watching is that an agent's next question sees the edit it just made; past a minute an explicit reindex would serve the user better.

### Observation follows use

A watcher is started when a project is running, woken by a request to that project's route, and released when the project stops or goes idle. Releasing it records a gap, so the next consumer knows to reconcile rather than trust a record that stopped being maintained.

## Alternatives considered

- **Trust the watcher and skip reconciliation.** Fewer moving parts, but it converts every lost event into a permanently wrong index with no signal.
- **Reconcile on a timer regardless.** Correct and simple, but it re-walks and re-digests the whole project on a schedule, which is the cost the journal exists to avoid.
- **Duplicate the ignore engine on the host.** Removes a dependency on the running service, at the price of two answers to one question and a drift whose only symptom is silence.
- **A recursive watch per project through a single native handle.** Windows can do this; macOS needs FSEvents and a cgo dependency in the host binary. Deferred rather than rejected — see the follow-up.
- **One shared debounce with no project override.** Simpler, but a generated-code-heavy repository and a small library want genuinely different windows.

## Consequences

### Positive

- A stale index is always distinguishable from a current one, in the tool response and not only in a log.
- A continuously running daemon updates an index from a handful of paths instead of a whole-tree walk.
- The exclusion policy has exactly one implementation, so the watcher cannot quietly disagree with the indexer.
- A project too large to watch degrades to a slower correct path instead of failing or lying.

### Negative

- Every daemon restart forces one reconciliation per project, which on a very large project is not cheap.
- A watcher cannot start until the project's service is answering, so the first moments after a start are unobserved — and recorded as such.
- The journal is another versioned document in the LCTK home to migrate.

### Follow-up

- Recursion is LCTK's, not the watcher library's: one native watch is registered per directory. On macOS that means one kqueue descriptor per watched path, which the watch budget bounds but does not remove. Measure the real ceiling on macOS and decide whether FSEvents, and the cgo dependency it brings to the host binary, is warranted.
- The journal has no consumer until Slice 2.2. Until then every project reports an incomplete record, which is accurate — nothing has yet brought an index up to date — but it means the freshness signal is not yet exercised in its interesting states.
- Bulk-change thresholds are set at 10,000 pending paths by value and not yet by measurement.
