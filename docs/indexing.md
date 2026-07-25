# Indexing and freshness

## Goal

LCTK maintains an up-to-date representation of code saved to disk. The user does not run a full reindex after routine edits.

Without its own IDE extension, LCTK indexes saved files. Unsaved editor buffers remain the responsibility of the IDE or Codex client.

## Change pipeline

```text
editor saves a file
→ host watcher receives a native filesystem event
→ event is normalized and written to the change journal
→ project idle timer is reset
→ debounce combines closely spaced events
→ batch is classified
→ affected indexes are updated
→ new index generation is published atomically
→ freshness state becomes ready
```

The watcher runs in the host daemon because native Windows and macOS events are more reliable and faster than watching a Docker Desktop bind mount.

The watcher is an accelerator, not the sole source of truth. After downtime, overflow, or a restart, the daemon reconciles the filesystem with the persistent index manifest.

## Configurable debounce

Debounce must be user-configurable. The range and configuration scope (machine default and/or project override) still need to be formalized.

The practical default under discussion is 3–5 seconds after the most recent change. Each new relevant event restarts the timer. The exact default has not yet been accepted.

Debounce delays updates to persistent indexes, but not file saves or ordinary source reads. While a batch is pending or being processed, responses show pending changes and freshness lag.

## Layered updates

A routine file change does not trigger a full repository rebuild:

- exact search: replace the changed file's document or segments;
- AST: parse the file again;
- symbols/LSP: update the document and affected compilation units;
- semantic: remove the file's previous chunks and add AST-aware chunks for the new version;
- graph: recompute relationships for affected symbols and modules;
- repository map: update local summaries and centrality when the change is significant.

Generation publication must prevent partially consistent mixed state from being returned. Different backends may have different generations or freshness states if the response makes this explicit.

## Batch classification

The indexer distinguishes:

- **small change** — incremental update;
- **large/bulk change** — targeted or repository rescan;
- **watcher overflow/unknown gap** — manifest reconciliation;
- **schema mismatch/corruption** — rebuild of the specific index.

Thresholds must have safe defaults. It has not been decided whether advanced thresholds will be user-configurable or remain engine-owned.

Special events:

- create/modify/delete/rename;
- branch checkout;
- rebase/merge;
- lockfile and build-configuration changes;
- bulk generation;
- Git worktree changes.

## Initial indexing

Initial indexing proceeds in layers:

1. project metadata and file inventory;
2. exact search;
3. AST/file outlines;
4. symbols/LSP availability;
5. semantic chunks/embeddings;
6. graph and repository map enrichment.

Early capabilities become available while heavyweight layers continue to build. Multi-hour CPU-only indexing of a large project is acceptable when progress is reported and the resource mode is controlled.

## Resource modes

The user selects a background-load mode, for example:

- `quiet` — minimal impact on interactive work;
- `normal` — balanced default;
- `fast` — finish indexing as quickly as possible.

Interactive MCP calls must take priority over background indexing in `quiet` and `normal` modes.

The embedding inference process may be a shared compute resource across projects. Project chunks, vector collections, and metadata remain separate.

## Freshness contract

Every index-dependent response must report the following compactly:

- source commit/branch;
- dirty state;
- index generation/version;
- pending file count or lag;
- freshness (`fresh`, `updating`, `stale`, `partial`);
- backend provenance;
- timestamp of the last successful update.

Example state:

```json
{
  "commit": "abc123",
  "dirty": true,
  "freshness": "updating",
  "pending_files": 3,
  "indexes": {
    "exact": { "state": "ready", "generation": 42 },
    "semantic": { "state": "updating", "generation": 41 },
    "graph": { "state": "stale", "lag_seconds": 12 }
  }
}
```

The system must not silently return a stale result as fully current.

## Persistence

Project volumes store:

- engine index data;
- index manifests;
- content hashes/file inventory;
- change journal checkpoint;
- schema and engine versions;
- last indexed commit/dirty snapshot;
- rebuild reason and status.

Updates must support atomic commit or swap where the backend permits. After a crash, the previous committed generation remains readable, or the index is explicitly marked as corrupt.

## Exclusions

At minimum, the following paths are excluded by default:

```text
.git
node_modules
dist
build
coverage
.venv
vendor
```

The manifest may add project-specific exclusions. Generated and dependency paths generally must not reset the idle timer or create an indexing storm, but the precise relationship among watcher exclusions, test artifacts, and explicitly included generated code requires a separate policy.
