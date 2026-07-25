# ADR-0011: Zoekt exact-search backend behind an LCTK adapter

- Status: accepted
- Date: 2026-07-25
- Deciders: project maintainers

## Context

LCTK requires a persistent project-scoped exact-search backend for the future stable `exact_search` tool. It must index the current saved working tree, including dirty and untracked files; support literal and regular-expression queries, path and language filters, bounded routine updates, persistent restart, and offline reconciliation; run in isolated Linux project containers on amd64 and arm64; and remain compatible with Apache-2.0 distribution.

Slice 0.3 evaluated Zoekt, Livegrep, and OpenGrok under one tracked contract, with ripgrep as the independent correctness oracle. The measured results are recorded in `docs/spikes/search-backend-evaluation-results.md`.

Zoekt passes the hard gates through a small working-tree adapter. Livegrep persists immutable finalized snapshots but lacks bounded post-finalization file mutation. OpenGrok does not provide the required one-file saved-working-tree update contract or arbitrary exact source-line literal/regex equivalence, and its official packaging evidence does not satisfy the arm64 gate.

Zoekt's relevant low-level `index` package is explicitly non-public upstream and uses Unix-specific APIs. Delta shards also accumulate and require compaction. Those constraints make a narrow pinned adapter and Linux container boundary mandatory.

## Decision

Use Zoekt as LCTK's persistent exact-search engine, pinned to an exact upstream revision and accessed only through an LCTK-owned internal adapter.

The per-project Linux search service will:

- enumerate and read files from the read-only `/workspace` mount instead of relying on Git objects;
- maintain an LCTK-owned content-hash manifest, generation metadata, exclusions, and offline reconciliation;
- build Zoekt base and delta shards in the project's persistent state volume;
- translate create, modify, delete, and rename batches into Zoekt delta additions and tombstones;
- execute structured literal, regex, filename, and language queries while returning bounded normalized results;
- stage full generations and atomically publish the active generation;
- compact or fully rebuild according to bounded delta-count, delta-size, schema-version, and corruption policies;
- translate backend and lifecycle failures into LCTK typed errors.

The portable host daemon owns project identity, watcher/journal delivery, lifecycle, and route-bound scope. Zoekt code is not linked into the Windows/macOS host executable. The public MCP API remains backend-independent, and no Zoekt-specific query or result type crosses the stable adapter boundary.

## Alternatives considered

### Livegrep

Rejected for this role. Its persisted index is a finalized snapshot. Routine saved-file create, modify, delete, and rename operations require rebuilding the snapshot, which fails the bounded incremental-update hard gate.

### OpenGrok

Rejected for this role. Its Lucene/analyzer query semantics do not guarantee arbitrary exact source-line literal and regex equivalence, live dirty updates depend on project traversal rather than a bounded public one-file contract, and official arm64 image evidence was insufficient. Its JVM/Tomcat/Python/ctags operational footprint is also a poor fit for one isolated service per project.

### ripgrep

Retained as a correctness oracle and possible diagnostic fallback only. It scans source and does not provide the persistent indexed-search requirement. Selecting it as the primary backend would require a separate ADR changing that product requirement.

### Custom persistent engine

Rejected. Implementing LCTK's own trigram or suffix index would duplicate a mature search engine, substantially enlarge correctness and maintenance risk, and violate the decision rule while Zoekt passes the hard gates.

## Consequences

### Positive

- Exact literal, regular-expression, path, and language searches use a mature persistent code-search engine.
- Dirty and untracked saved files are indexed because LCTK controls filesystem enumeration.
- Base and delta shards support bounded routine updates without rewriting the complete index.
- Project-specific volumes and process boundaries preserve isolation and restart state.
- The backend can be replaced later without changing the public MCP tool schema.
- Apache-2.0 licensing aligns with LCTK distribution.

### Negative

- LCTK depends on an upstream package explicitly outside Zoekt's stable public API.
- Exact revision pinning, compatibility tests, and deliberate upgrades are mandatory.
- Native host compilation is unavailable; exact search depends on the Linux project container.
- Delta accumulation requires an LCTK-owned compaction/full-rebuild policy.
- A safe generation-publication layer adds implementation complexity beyond the spike adapter.
- Initial Docker Desktop target certification remains future work.

### Follow-up

- Implement the production internal adapter and staged generation store in Slice 1.4.
- Implement watcher journal, debounce, batching, and reconciliation scheduling in Slice 2.2.
- Define and test compaction thresholds before persistent search is declared complete.
- Publish and test Linux amd64/arm64 project-service images from the release pipeline.
- Add target-host Docker Desktop lifecycle verification and backend-upgrade compatibility fixtures.
