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

Rules come from three files, each using the familiar ignore syntax, applied in this order within every directory. A later rule beats an earlier one, so a file further down the list can both add exclusions and re-include with `!`.

| File | Role | Tracked |
|---|---|---|
| `.gitignore` | A default, not an authority | yes |
| `.lctkignore` | The project's decision about **indexing** | yes |
| `.lctkignore.local` | The same decision for one machine | no |

A version-control ignore file answers "what should not be committed". That is a different question from "what should not be indexed", and the two disagree in a specific and common way: a local scratch directory is deliberately uncommitted and is exactly the sort of thing its owner wants to search. So `.gitignore` is consulted because it is usually right and always present, and `.lctkignore` has the last word because only the project knows what it wants searched:

```gitignore
# .lctkignore
!.work/        # uncommitted, but I search it constantly
fixtures/big/  # committed, but nothing here is worth a match
```

`.lctkignore.local` is untracked by convention and is applied last, so a personal choice does not end up in a shared file. It mirrors the manifest's local override described in [security](security.md).

A project whose version-control rules say nothing useful about indexing can start from a clean slate: a first line of `!/**` in `.lctkignore` re-includes everything, and the rules after it narrow the set again.

All three are honoured in nested directories, where they apply to their own subtree and not to siblings.

Enumeration comes from the filesystem rather than from Git objects, so a file that is saved but never committed is indexed. That is a statement about content, not about scope: a directory the project has told LCTK to ignore is not part of the project.

The index status reports which of the three files were actually found, so the rules in effect are visible rather than inferred from what went missing.

LCTK adds a short default list, applied *before* all of them so the project can overrule any of it:

```text
node_modules  .venv  venv  __pycache__  .mypy_cache  .pytest_cache
.tox  .gradle  .turbo  .next  .nuxt  .parcel-cache
```

The list is deliberately short. Names like `dist`, `build`, `target`, and `vendor` are derived output in some projects and real source in others; excluding them by default would lose code silently, and a project that treats them as output has already said so in its ignore file.

Version-control metadata — `.git`, `.hg`, `.svn` — is excluded unconditionally and cannot be re-included.

Symbolic links are skipped rather than followed. Following one is the ordinary way out of a read-only mount, and the mount is the boundary the project service is trusted to stay inside.

The number of entries excluded by ignore rules and by the file-size limit is reported in the index status, so the effect of an exclusion is visible rather than silent.

The manifest may add project-specific exclusions. Generated and dependency paths generally must not reset the idle timer or create an indexing storm, but the precise relationship among watcher exclusions, test artifacts, and explicitly included generated code requires a separate policy.

## Persistent exact index

As implemented in Slice 1.5, the exact-search index is a directory of published generations inside the project volume, with a `current` link naming the live one.

A build never writes into the published generation. It stages a new directory, hard-links the previous shards where a delta build needs them, writes its state, and then replaces one symlink. A concurrent search therefore reads either the whole previous generation or the whole new one, and a crash mid-build leaves the previous generation intact rather than a half-written index.

Delta depth is bounded. The engine resolves a query across every shard in the generation, so an unbounded pile of deltas would slow every future search, not only the next update. Past the threshold the next update is escalated to a full rebuild. The shipped policy is 32 delta generations, 2 retained generations, and a 1 MiB per-file limit; a project may raise the file limit and the delta threshold through the environment.

A published generation written by a different schema version is treated as corrupt rather than read, because the shard format belongs to the engine and LCTK does not attempt to migrate it. A corrupt index is reported as a typed error, never as an empty result: answering "no matches" would look like a correct answer about the project.
