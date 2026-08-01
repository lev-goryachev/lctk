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

## The change journal

As implemented in Slice 2.1, the host keeps one journal per project in the LCTK home. It makes a single claim: **every change since its checkpoint is in the pending list, or a gap says otherwise.** [ADR-0015](adr/0015-change-observation-is-complete-or-declared-incomplete.md) records why that claim is the whole design.

The journal holds a monotonic sequence, a checkpoint the consumer advances, the index generation that existed at the checkpoint, one pending entry per changed path, and an optional gap.

Deduplication is by path. A file saved fifty times is one change to apply, not fifty.

A gap is recorded whenever observation *could* have been incomplete — not when something is proven lost:

| Reason | What happened |
|---|---|
| `observation_started` | The journal was loaded, so the period before it was unobserved |
| `observation_suspended` | The watcher was released because the project stopped or went idle |
| `watcher_overflow` | The native watcher lost events |
| `watch_capacity_exceeded` | The project has more directories than the watch budget |
| `watch_set_incomplete` | The service would not describe the whole project |
| `directory_unwatchable` | A directory exists but could not be registered |
| `consumer_backlog` | Events arrived faster than they were taken |
| `bulk_change` | More paths changed than the journal will track |
| `journal_unreadable` | The stored document was reset |

A gap is a latch. It keeps the earliest reason, since that is the moment from which the record stopped being complete, and it is cleared only by a consumer that reconciled the filesystem with the index — and only when the gap it closes is the one it set out to close, not one that opened while it worked.

Loading a journal always records a gap. Nothing on disk can establish that a project was unchanged while the daemon was not running. What persistence buys is that a continuously running daemon never has to reconcile, and that work observed but not yet applied is not lost when the process ends.

`lctk project watch PROJECT` reads the journal, and `--follow` streams normalized events for diagnosis without writing anything.

## What is watched

The host asks the project's own service which directories to observe. The service owns the exclusion policy, so it is the only thing that can answer without disagreeing with the indexer; see [ADR-0015](adr/0015-change-observation-is-complete-or-declared-incomplete.md).

The host hard-codes only `.git`, `.hg`, and `.svn`, which is safe because those are exactly the rules a project cannot override.

A newly created directory is adopted immediately, together with whatever is already inside it. That walk is not tidiness: a tool that writes a whole tree at once fills the directory before a watch can be placed on it, and without the walk those files stay invisible until something else touches them.

Watching costs one native handle per directory, so a project may hold at most `watch.max_watched_directories` of them, 20,000 by default. Past the budget the watcher observes part of the tree and records a capacity gap, which routes the project to reconciliation rather than to either failure or silence.

## Configurable debounce

The shipped default is **3 seconds** after the most recent change, with each new change restarting the wait, and a 30-second ceiling on how long continuous editing may defer an update.

Three layers, each narrower than the last:

| Layer | Where | Role |
|---|---|---|
| Shipped default | `internal/hostsettings` | 3 s window, 30 s ceiling |
| Machine policy | `settings.json` in the LCTK home | What the machine owner wants |
| Project proposal | `index.debounce_ms` in the manifest | What the repository suggests |

The project's value is a proposal, not a setting: the host clamps it to between 200 ms and 60 seconds. The floor exists because an editor save is often a write to a temporary file followed by a rename, and reacting between the two indexes a file that is about to be replaced. The ceiling exists because the point of watching is that an agent's next question sees the edit it just made.

`lctk settings show` prints the policy in force and the path of the file that would change it.

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
- freshness (`fresh`, `updating`, `stale`, `unknown`);
- backend provenance;
- timestamp of the last successful update.

Freshness is never optimistic. `unknown` is reported for a project nothing is watching, because "nobody looked" is not evidence that nothing changed, and an agent told `fresh` will not check again. An incomplete record reports `stale` with the gap reason attached, and the pending count is then a lower bound rather than a total.

`project_info` carries this as an `index.freshness` verdict alongside a `changes` block naming what the host has observed:

```json
{
  "index": { "ready": true, "generation": 12, "file_count": 169, "freshness": "stale" },
  "changes": {
    "watching": true,
    "pending": 1,
    "complete": false,
    "gap_reason": "observation_started",
    "debounce_seconds": 3
  }
}
```

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

The change journal itself lives on the host rather than in the project volume, one owner-only document per project under `journals/` in the LCTK home. It belongs with the registry and the grants for the reason given in [security](security.md): it is host state about a project, not project content, and it must not be reachable from inside a repository. It is written atomically at each settle, so an interrupted write cannot leave a shorter pending list that would read as complete.

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
