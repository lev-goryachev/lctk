# ADR-0005: Host watcher and incremental indexing

- Status: accepted
- Date: 2026-07-24
- Deciders: project maintainers

## Context

Indexes must automatically reflect saved changes. File events inside Docker Desktop bind mounts may be slow or unreliable, especially for arbitrary Windows drives and macOS folders. A full rebuild on every save is unacceptable, and watcher events may be lost during downtime or overflow.

## Decision

The host daemon watches registered roots through native OS APIs, records a normalized change journal, and sends debounced batches to the project indexer. Debounce is user-configurable.

Routine batches are updated incrementally. Bulk operations switch to rescan and reconciliation. The persistent manifest and hashes remain the source of truth for recovery; the watcher is not considered sufficient to ensure integrity after a restart.

A new change to an indexed file counts as user activity, resets the on-demand idle timer, and starts the project stack when necessary. An old uncommitted diff does not constitute activity by itself.

## Alternatives considered

- **Watcher only inside the container.** Simpler topology, but worse Docker Desktop behavior and on-demand wakeup.
- **Periodic full scan.** Reliable, but expensive and introduces high freshness latency.
- **Git status as the sole source.** Does not reflect all filesystem semantics and is poorly suited to fast incremental updates.
- **IDE extension events.** Can see editor buffers, but couple core infrastructure to a specific IDE.

## Consequences

### Positive

- Native, fast file events.
- Indexing works with any editor.
- The daemon can wake the on-demand stack before the next MCP call.
- Recovery does not depend on perfect event delivery.

### Negative

- Separate watcher adapters and tests are required for Windows and macOS.
- Build and dependency noise and event storms must be suppressed.
- The change journal and index manifest require a versioned persistent schema.

### Follow-up

- Use `fsnotify` for the Go watcher adapter. Slice 2.1 implements the normalization, coalescing, overflow handling, and debounce this ADR describes, and [ADR-0015](0015-change-observation-is-complete-or-declared-incomplete.md) settles the questions it left open: what happens when observation is incomplete, who decides what is watched, and how debounce is configured.
- Rename and coalescing semantics are covered by automated tests on both target platforms; a rename arrives as a removal of the old path and a write of the new one. Deletion of a whole directory is reported as such, because once it is gone the filesystem can no longer say what it was.
- Debounce default and configuration scope: accepted in ADR-0015 as 3 seconds, machine default in the host settings file, project proposal in the manifest, clamped by the host.
- Reconciliation after downtime, and the excluded-path activity policy, belong to Slice 2.2 and are not yet implemented.
- Bulk-change thresholds are set by value rather than by measurement, and remain open.
