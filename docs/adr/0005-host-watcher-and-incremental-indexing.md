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

- Use `fsnotify v1.9.0` for the Go watcher adapter. The Slice 0.1 proof verifies basic event delivery; NTFS/APFS rename, coalescing, overflow, normalization, debounce, and reconciliation behavior remain to be validated.
- Measure rename and coalescing semantics on NTFS and APFS.
- Accept a default debounce and configuration scope.
- Define bulk-change thresholds and the excluded-path activity policy.
