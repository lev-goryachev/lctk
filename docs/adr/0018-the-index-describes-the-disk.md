# ADR-0018: The index describes the disk, and a write that changed nothing costs nothing

- Status: accepted
- Date: 2026-08-03
- Deciders: project maintainers

## Context

Most coding clients hold a diff before it becomes a file. Some show a proposal and write it only once a human accepts; others write immediately and offer a revert. In both shapes there is a state of the work that the filesystem does not describe: in the first, an edit that exists only inside the client; in the second, an edit on disk that may be undone in a minute.

LCTK observes the filesystem. [ADR-0015](0015-change-observation-is-complete-or-declared-incomplete.md) settled how honestly it reports what it saw, and [ADR-0005](0005-host-watcher-and-incremental-indexing.md) how a change reaches the index. Neither answers what LCTK owes a caller whose work is in one of those two states, and the second shape turns out to cost something measurable.

## Decision

### An unapplied edit is out of scope, and the scope is stated

The index describes files as they are written to disk. An unsaved buffer and an unapplied patch are not indexed, and no channel is added for importing one.

The only way to import one is to let a client declare what a file contains. That is refused for the same reason a manifest cannot mount a path or supply a secret: it is untrusted input with authority it should not have. Here the specific failure is worth naming — a client could put text into the index that never existed on disk, and a second client would find it by search and have no way to tell it from the project's own code.

The cost of refusing is that a client holding a patch is the only party that knows about it. That is acceptable; what is not acceptable is a caller reading `freshness: fresh` as "everything I know about is here". So the scope is stated where an agent will read it: in the `exact_search` tool description, in the freshness contract, and on the field itself.

### An edit on disk is the project's content, whoever wrote it and however briefly

An edit written to disk and awaiting a human's approval is indexed, searchable, and reported by `git_status` as a working-tree change.

LCTK does not try to distinguish it from a developer's own work in progress. The filesystem does not record who wrote a file or why, so any distinction would be a guess, and a wrong guess would mean either hiding real code from a search or labelling a developer's work as a machine's.

### A write whose content already matches the index is dropped

Before applying a written path, the service compares the file's content digest against the digest recorded for that path in the published index. On a match the change is dropped: nothing is retracted, nothing is added, and no generation is published.

This is not a micro-optimization. Delta depth is the budget that forces the next full rebuild, so a no-op delta is not free work — it moves a rebuild closer. Writes that change nothing are ordinary: a formatter with nothing to reformat, an editor writing on focus loss, a generator rewriting identical output, `git checkout` and back, and the revert shape above.

Escalation to a full rebuild is judged on the same effective count. Before this, eight paths submitted with no edits among them was 40% of a twenty-file index and rebuilt the whole thing.

Filtering is per path within a batch, so one real edit among eight submitted paths is a one-file delta rather than a bulk rebuild.

### What is dropped is reported

`Applied` carries what a batch did: paths changed, writes that changed nothing, whether it escalated, and how many generations it consumed. The service reports it on `/index`, and the daemon logs `unchanged` even when zero.

A filter that works silently cannot be distinguished from a filter that is not running. The count is also the measurement the slice is closed on.

## Alternatives considered

- **Accept file content from a client so an unapplied patch can be searched.** Discussed above: it makes the index assert things about the disk that are not true, and the damage lands on a different client than the one that lied.
- **Let a client pause indexing while a human reviews.** A lock, and a client that crashes holding it leaves the project blind. The failure is silent and is discovered when somebody receives a stale result reported as fresh.
- **Maintain a shadow index for a proposed patch.** Answers "what would search look like if this were applied". Expensive, speculative, and nobody has asked for it.
- **Compare modification time or size instead of content.** Cheaper, and wrong in both directions: a rewrite with the same size is common, and a touched file with unchanged content is exactly the case this is meant to catch.
- **Compare digests only at the batch level.** Simpler and it would miss the ordinary batch: several files saved together with one of them actually edited.
- **Leave it as it was.** Correct, and it charges an editor's autosave against the rebuild budget.

## Consequences

### Positive

- A save that changed nothing consumes no generation, no delta depth, and no rebuild budget.
- An edit reverted before the index catches up costs nothing at all.
- A large batch is escalated on what it changed rather than on its size, so a checkout and its undo no longer force two full rebuilds.
- The scope of a search result is stated rather than assumed, in the place an agent reads.

### Negative

- Every written path in a batch is read and hashed before the batch can be judged bulk, so a batch that does escalate has paid for a read it did not use. Bounded by the batch, and cheaper than the rebuild it may avoid.
- An edit reverted *after* the index caught up still costs two generations. This is correct rather than fixable: the index held the other content and answered searches from it.
- A client holding an unapplied patch gets no help from LCTK about it, and there is no plan to change that.

### Follow-up

- An agent still cannot ask "what changed since I last looked". The journal is a work queue and forgets an entry once it is applied, so answering it needs retention that does not exist yet — a separate decision, not an extension of this one.
