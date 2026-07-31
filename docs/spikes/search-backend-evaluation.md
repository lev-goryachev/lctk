# Search backend evaluation contract

## Status

Accepted Slice 0.3 test contract. Backend selection remains open until the measured results and proposed recommendation are reviewed with the maintainer.

Evaluation date: 2026-07-25.

## Candidates

The same externally observable scenario is evaluated against:

- Zoekt commit `2b2ce2e398e6bee68d67143f567b6c6199340c7f`;
- Livegrep commit `923d5ad71dfe60900e6c2017b2fa4a5ff902ad71`;
- OpenGrok release 1.14.15 at commit `6ba0649842096c340e6d4c7400af3f3b6cc1b2bf`;
- `ripgrep` 15.2.0 as a correctness oracle and diagnostic baseline only.

All production candidates must be persistent indexed-search engines. A library or scan-only command does not qualify as the selected backend. `ripgrep` receives no credit for persistence and cannot be recommended without a separate ADR that changes the persistent-search requirement.

Candidate-specific APIs, result shapes, ranking, path formats, and update commands are normalized by evidence adapters. An adapter that implements its own persistent content index is counted as a custom search engine rather than attributing that capability to the candidate.

## Fixtures

The tracked harness deterministically creates two independent working trees under an ignored repository-local evaluation directory:

- `alpha`, containing TypeScript, JavaScript, Python, Go, Rust, C, C++, Markdown, JSON, dependency-like, binary, Unicode-path, and space-containing-path fixtures plus an included synthetic measurement corpus;
- `beta`, containing unique sentinels used to detect cross-project result leakage.

The fixture includes:

- repeated literal tokens with deterministic ordering;
- regex-only patterns spanning multiple languages;
- mixed-case content;
- long lines and bounded-context cases;
- files under `.git`, `.hg`, `.svn`, `node_modules`, `dist`, `build`, `coverage`, `.venv`, `vendor`, and `generated`;
- an untracked dirty file and modifications not committed to Git;
- enough synthetic source files under the included `corpus/` directory to produce measurable indexing, query, update, and disk behavior without claiming a stress-scale result.

The expected result inventory is generated with the fixture and checked by `ripgrep` using the same explicit exclusion policy. Oracle matching verifies result membership; backend-specific ranking is not required to match.

## Normalized query contract

The evidence adapter accepts:

```json
{
  "pattern": "immutableRoute",
  "mode": "literal",
  "case_sensitive": true,
  "path_globs": ["src/**/*.ts"],
  "languages": ["typescript"],
  "limit": 50,
  "cursor": ""
}
```

`mode` is either `literal` or `regex`. Globs are project-relative path filters, not content wildcards. Language filtering uses an LCTK-owned extension-to-language map and may be pushed down to the backend or applied to complete backend results by a bounded adapter stage. The evaluation records which layer performs each filter.

Normalized matches contain:

```json
{
  "path": "src/router.ts",
  "line": 12,
  "column": 7,
  "preview": "const immutableRoute = ...",
  "match": "immutableRoute"
}
```

Paths must be slash-normalized, project-relative, and must never escape the registered root. Line and column numbers are one-based. Preview and match text are bounded. The Slice 0.3 schema is evidence for the future `exact_search` adapter; it is not yet the public MCP v1 schema.

## Required scenario

For each production candidate, the harness verifies:

1. a clean initial index includes saved files from the mounted working tree rather than only remote branches;
2. dirty tracked files and untracked files are searchable;
3. literal and regex results match the oracle after applying the same exclusions;
4. path-glob and language filters produce the expected subset;
5. excluded dependency, generated, VCS, coverage, and virtual-environment paths do not appear by default;
6. alpha queries never return beta paths or sentinels;
7. a saved file creation becomes searchable through an incremental or bounded targeted update;
8. a saved modification removes old matches and adds new matches;
9. deletion removes matches;
10. rename removes the old path and adds the new path;
11. stopping and starting the engine with the same persistent state preserves queryability without a full reindex;
12. changes made while the engine is stopped can be reconciled without discarding a valid unchanged index;
13. malformed regex, invalid cursor, excessive limit, and unavailable/corrupt index conditions can be translated into bounded typed adapter failures;
14. source is mounted read-only at `/workspace`, index state is stored outside the source tree, and no candidate receives another project mount or the Docker socket.

A candidate passes incremental indexing only when a routine one-file create, modify, delete, or rename can update a bounded subset of persistent index state. Rebuilding every shard or the complete repository for every routine event is recorded as a hard-gate failure even when the corpus is small.

Slice 0.3 verifies backend and adapter capability. Slice 2.2 remains responsible for implementing the production host-watcher, journal, debounce, batching, reconciliation, and generation-publication pipeline from ADR-0005.

## Restart definitions

The evaluation distinguishes:

- **process restart** — stop and start the search process while retaining index files;
- **container restart** — replace the candidate container while retaining the index volume;
- **offline catch-up** — modify the mounted source while the engine is stopped, then reconcile;
- **rebuild** — discard candidate index data and index all included files again.

A restart passes only when logs and file timestamps show that existing persistent index data was reused. Offline catch-up may scan an LCTK-owned manifest to identify changed files; it must not silently claim backend-native change detection.

Docker Desktop on the current Windows host is direct runtime evidence. A Linux arm64 image manifest or successful cross-platform image build is packaging evidence only, not macOS 13 or Docker Desktop certification.

## Adapter ownership

The future LCTK exact-search adapter owns:

- project scope and `/workspace` root assignment;
- exclusion and extension-to-language policy;
- query validation, limits, cursor signing/versioning, and cancellation;
- normalized project-relative paths and bounded snippets;
- result deduplication and deterministic pagination;
- index generation, freshness, provenance, and source-state metadata;
- the content inventory/hash manifest and offline reconciliation;
- typed error translation and health reporting;
- candidate process lifecycle and persistent schema compatibility checks.

The backend owns its index format, query execution, and supported targeted update mechanism. Backend state is never the sole authority for project identity or filesystem completeness.

## Typed failure contract

The spike uses this provisional adapter envelope:

```json
{
  "code": "INVALID_PATTERN",
  "message": "The regular expression is invalid.",
  "retryable": false,
  "backend": "zoekt",
  "generation": 3,
  "request_id": "..."
}
```

Minimum codes under test:

- `INVALID_PATTERN`;
- `INVALID_CURSOR`;
- `LIMIT_EXCEEDED`;
- `INDEX_NOT_READY`;
- `INDEX_CORRUPT`;
- `BACKEND_UNAVAILABLE`;
- `INTERNAL_ERROR`.

This envelope is a spike contract, not yet the public v1 schema.

## Measurements

Measurements are repeated after warm-up and reported with the environment and fixture inventory:

- candidate source revision, release, license, and image digest;
- image platforms and compressed/uncompressed size where available;
- cold initial indexing time and peak memory/CPU;
- persistent index size relative to included source bytes;
- process/container restart readiness without rebuild;
- one-file create, modify, delete, and rename update latency and bytes rewritten;
- offline catch-up latency;
- literal, regex, path-filtered, and language-filtered query correctness;
- warm query latency over at least 100 sequential queries per query class;
- idle resident memory and process count;
- shutdown behavior and leaked processes/containers;
- configuration volume and LCTK-owned adapter behavior required.

Absolute timings are evidence for this machine and fixture only. They are not product performance guarantees.

## Scoring

Hard gates are evaluated before weighted scoring.

### Hard gates

A production candidate must:

- provide a persistent content index reusable after process/container restart;
- search the live saved working tree, including dirty and untracked files;
- return correct literal and regular-expression matches;
- support bounded incremental or targeted updates for create, modify, delete, and rename;
- support project-relative path filtering directly or through a bounded adapter;
- permit deterministic language filtering directly or through a bounded adapter;
- keep index data outside the read-only source mount and isolate projects;
- publish or permit a reproducible Linux amd64 and arm64 build;
- use a license compatible with Apache-2.0 distribution;
- expose enough health and failure information for typed LCTK diagnostics.

A failed hard gate is acceptable only when a small adapter can supply it without implementing another persistent content index or routinely rebuilding the entire repository.

### Weighted criteria

| Criterion | Weight |
|---|---:|
| Correctness and query coverage | 25 |
| Incremental update and reconciliation fit | 25 |
| Persistence and recovery | 15 |
| Performance and resource footprint | 15 |
| Adapter complexity and stable API fit | 10 |
| Maintenance, packaging, and license posture | 10 |

Scores use a five-point scale and cite either a reproducible test or an exact upstream source/documentation reference.

## Decision rule

The recommendation chooses the simplest persistent backend that passes the hard gates and preserves ADR-0003, ADR-0004, and ADR-0005. Query speed alone does not compensate for snapshot-only indexing, full rebuilds on routine edits, project leakage, unsupported arm64 packaging, or an adapter that becomes a second search engine.

The evaluation results and recommendation are presented to the maintainer before an ADR is accepted, production code adopts a backend, or any Slice 0.3 changes are committed or pushed.
