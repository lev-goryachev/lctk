# Search backend evaluation results

## Status

Slice 0.3 measured result and accepted recommendation. The maintainer approved ADR-0011 on 2026-07-26.

Evaluation date: 2026-07-25.

## Recommendation

The accepted backend is **Zoekt behind a narrow LCTK-owned working-tree adapter**, pinned to exact revision `2b2ce2e398e6bee68d67143f567b6c6199340c7f`.

Zoekt is the only evaluated backend that passes every hard gate without making LCTK implement a second persistent content index. The recommendation is conditional on production work that the spike intentionally does not claim:

- stage complete generations and atomically publish an active-generation pointer;
- compact or fully rebuild after a bounded number/size of delta shards;
- pin and compatibility-test Zoekt's non-public low-level `index` package;
- run indexing inside Linux project containers rather than compiling it into the Windows/macOS host binary;
- retain LCTK ownership of filesystem inventory, exclusions, reconciliation, project identity, paths, cursors, typed failures, and freshness.

Livegrep and OpenGrok fail hard gates before weighted scoring. `ripgrep` remains the independent correctness oracle and may be a diagnostic fallback, not the persistent backend.

## Pins and environment

| Item | Evidence |
|---|---|
| Host | Windows 10 Pro 22H2 x86-64 |
| Container runtime | Docker Desktop 4.77.0; Engine 29.5.3; Linux amd64 WSL2 |
| Go | 1.25.9 |
| Build image | `golang:1.25.9-bookworm` at `sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3` |
| Zoekt | `2b2ce2e398e6bee68d67143f567b6c6199340c7f`; Apache-2.0 |
| Livegrep | `923d5ad71dfe60900e6c2017b2fa4a5ff902ad71`; BSD-3-Clause |
| OpenGrok | release 1.14.15, commit `6ba0649842096c340e6d4c7400af3f3b6cc1b2bf`; CDDL-1.0 |
| ripgrep oracle | 15.2.0 Linux x86-64 musl archive; verified SHA-256 `33e15bcf1624b25cdd2a55813a47a2f95dbe126268203e76aa6a585d1e7b149c` |
| Fixture | 2,012 included alpha files, 104,414 bytes; one dirty tracked and one untracked file; isolated one-file beta project |

The alpha corpus is deliberately file-count-heavy and byte-light. Disk ratios and Docker Desktop bind-mount timings are useful comparative evidence, not product performance claims.

## Hard-gate results

| Gate | Zoekt | Livegrep | OpenGrok |
|---|---|---|---|
| Persistent index reuse | Pass: directory searcher opens existing `.zoekt` shards | Pass: persisted memory-mapped snapshot | Pass: persistent Lucene index |
| Saved dirty/untracked tree | Pass through LCTK filesystem enumeration | Pass in filesystem indexing mode | Conditional on project traversal |
| Exact literal and regex | Pass against ripgrep oracle | Pass | Fail: Lucene term-oriented semantics do not preserve arbitrary source-line equivalence |
| Bounded create/modify/delete/rename | Pass with delta shards and tombstones | **Fail:** finalized snapshot has no routine mutation path; rebuild required | **Fail:** live dirty changes require whole-project traversal; no public one-file update contract |
| Path/language filters | Pass with native query pushdown | Technically adaptable | Does not repair query/update failures |
| Isolated external index state | Pass | Pass | Pass |
| Reproducible Linux amd64/arm64 | Pass: tests and scratch-image builds completed on both | Buildable, but incremental gate already fails | **Fail:** official image evidence is amd64-only and runtime is materially heavier |
| Apache-2.0-compatible distribution | Pass: Apache-2.0 | Pass: BSD-3-Clause | Conditional: CDDL-1.0 separate-component obligations |
| Typed diagnostics fit | Pass through adapter | Adaptable | Weak operational fit |

Hard-gate source evidence:

- Zoekt `index.Options.IsDelta` defines delta builds, and `Builder.MarkFileAsChangedOrRemoved` tombstones changed or removed paths in older shards.
- Zoekt explicitly documents package `index` as non-public and not recommended for external reliance. This is a real maintenance risk, not hidden adapter stability.
- Livegrep's `code_searcher::finalize()` freezes the built index; the exposed indexing flow builds and finalizes a complete snapshot rather than mutating individual files afterward.
- OpenGrok's operational model runs project indexing around Lucene and analyzer terms. It does not expose the bounded saved-file mutation and arbitrary exact-match contract required here.

Candidates that fail a hard gate are not runtime-benchmarked further: faster full-snapshot construction would not satisfy routine incremental indexing.

## Zoekt correctness and lifecycle evidence

The tracked harness indexes bytes read from the current saved working tree, not Git objects. The baseline included and found both `src/router.ts` modified after the fixture commit and untracked `src/untracked.ts`.

All measured literal, regex, path/language-filtered, dirty-file, untracked-file, post-delta, offline-reconciliation, and beta-isolation comparisons had the same normalized membership as ripgrep. Empty expected sets for deleted content and cross-project alpha content also matched.

- Path and language filters are pushed into Zoekt's structured query tree.
- Paths are slash-normalized and project-relative.
- Lines and columns are one-based.
- Previews are bounded to 512 bytes.
- Default inventory excludes `.git`, `.hg`, `.svn`, `node_modules`, `dist`, `build`, `coverage`, `.venv`, `vendor`, and `generated`.
- Pagination cursors carry the manifest generation and become invalid after an update.
- Separate alpha and beta index directories prevent result leakage.

Fresh containers repeatedly opened existing persistent shards for query and benchmark commands. The initial base shard remained byte-identical across four targeted updates, proving that routine updates did not rewrite the complete base shard.

## Measurements

### Initial index and storage

| Metric | Result |
|---|---:|
| Included files | 2,012 |
| Included source bytes | 104,414 |
| Fresh full-index duration | 14,206.07 ms |
| Zoekt shard bytes | 434,466 |
| LCTK manifest bytes | 197,148 |
| Total persistent state | 631,614 bytes |
| Total state/source ratio | 6.05x |
| One indexing resource sample | 33.25 MiB, 14 PIDs, 16.52% reported CPU |

The resource value is a point-in-time sample during indexing, not a measured peak. Scratch evaluator image sizes were 10,008,647 bytes for Linux amd64 and 9,292,749 bytes for Linux arm64.

### Targeted updates

| Event | Duration | Manifest generation | Persistent shards |
|---|---:|---:|---:|
| Modify one file | 3,399.32 ms | 2 | 2 |
| Create one file | 3,499.59 ms | 3 | 3 |
| Delete one file | 3,683.00 ms | 4 | 3 |
| Rename one file | 3,576.05 ms | 5 | 4 |

Deletion produced a tombstone without a new content shard. Rename was submitted as old-path deletion plus new-path addition. The base-shard SHA-256 was unchanged before and after all operations.

These absolute latencies include fresh-container startup and Windows bind-mount overhead. Production batching and a resident per-project service should reduce fixed costs; Slice 2.2 owns that implementation.

### Warm search latency

One open directory-search session executed 100 sequential queries after warm-up.

| Query | Minimum | Median | p95 | Maximum | Mean |
|---|---:|---:|---:|---:|---:|
| Literal unique corpus marker | 4.67 ms | 6.38 ms | 9.06 ms | 68.68 ms | 7.29 ms |
| Regex `route_[0-9]{3}` | 4.64 ms | 5.69 ms | 8.28 ms | 10.09 ms | 6.09 ms |

### Offline reconciliation

While no adapter process was running, one file was modified, one created, and one deleted. Manifest reconciliation identified exactly those three paths, appended one delta shard, advanced generation 5 to 6, and retained the existing base/delta state.

- duration: 9,368.81 ms;
- included files after reconciliation: 2,012;
- persistent state after reconciliation: 643,015 bytes;
- all added and removed match sets agreed with ripgrep.

The time includes hashing every included file. A production host journal can make the common path cheaper, while the hash inventory remains the correctness fallback.

## Typed failure evidence

The adapter returned bounded typed errors for:

- malformed regex → `INVALID_PATTERN`;
- malformed or stale cursor → `INVALID_CURSOR`;
- limit greater than 1,000 → `LIMIT_EXCEEDED`;
- missing index → retryable `INDEX_NOT_READY`;
- invalid manifest → `INDEX_CORRUPT`.

`BACKEND_UNAVAILABLE` remains a production lifecycle translation because this library spike opens shards in-process. Unexpected Zoekt search failures map to `INTERNAL_ERROR`.

## Packaging evidence

The complete test suite passed in native Linux containers for both `linux/amd64` and `linux/arm64`. The tracked multi-stage Dockerfile produced runnable scratch images for both architectures from the same pinned source and Go image.

Native Windows compilation intentionally fails in upstream Zoekt's low-level index package because it uses Unix-specific APIs. This supports, rather than changes, the architecture: the host daemon remains portable Go, while exact-search indexing runs in each project's Linux container.

A local arm64 build under amd64 Docker Desktop uses emulation and is packaging evidence only. It does not certify Docker Desktop on macOS 13 arm64.

## Weighted score for passing candidates

Only Zoekt reached weighted scoring.

| Criterion | Weight | Score (1–5) | Weighted points |
|---|---:|---:|---:|
| Correctness and query coverage | 25 | 5 | 25 |
| Incremental update and reconciliation fit | 25 | 4 | 20 |
| Persistence and recovery | 15 | 4 | 12 |
| Performance and resource footprint | 15 | 4 | 12 |
| Adapter complexity and stable API fit | 10 | 2 | 4 |
| Maintenance, packaging, and license posture | 10 | 4 | 8 |
| **Total** | **100** |  | **81/100** |

The low stable-API score reflects deliberate dependence on a non-public upstream package. Persistence loses one point because production-safe staged generation publication and compaction are required but not implemented by this evidence adapter.

## Required production follow-up

Before Slice 1.4 can claim persistent `exact_search`:

1. wrap the pinned Zoekt revision behind an LCTK-owned internal interface;
2. build and publish a multi-platform project-service image in the release pipeline;
3. use generation directories and atomic active-generation publication so a failed full rebuild cannot remove the last readable generation;
4. define delta-count, delta-bytes, schema-version, and corruption triggers for compaction/full rebuild;
5. implement host watcher journal, debounce, batching, and catch-up in Slice 2.2;
6. add process health translation for `BACKEND_UNAVAILABLE` and cancellation tests;
7. repeat lifecycle verification on the declared Docker Desktop targets.

Spike code remains evidence. It must not be copied wholesale into the production service.
