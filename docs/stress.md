# Stage 7 stress evidence

## Claim boundary

These measurements were collected on 2026-08-05 on Windows 10 Pro 22H2 build 19045, an Intel Core i7-6800K with 12 logical processors and 32 GiB of host RAM, and Docker Desktop's Linux engine 29.5.3 with Compose 5.1.4. Docker exposed a 15.57 GiB memory limit to the containers.

The suite is parameterized through `scripts/stress.ps1` and uses the production search packages and schemas. It is evidence from one maintainer machine, not certification or a latency guarantee for every repository.

Storage matches the product boundary:

- exact-search source is a Windows bind mount containing ordinary, separately written files;
- the Zoekt index and semantic SQLite database are held in isolated Docker volumes, as project state is in production;
- each run creates a labeled volume, removes it after the run, and retains only machine-readable JSONL under the ignored `.artifacts/stress` directory.

An initial diagnostic incorrectly placed index/database state on the Windows evidence bind. Those figures are rejected rather than mixed into the release curve. The diagnostic exposed the storage-boundary error and the harness was corrected before acceptance.

## Semantic adapter

The semantic corpus writes one deterministic 768-dimensional normalized vector and one production-schema chunk per synthetic file. This deliberately excludes model inference time: the measurement isolates SQLite publication, the owned exact cosine adapter, lexical scoring, exact `total` accounting, top-K memory, and database size. Stage 5 records model and batching evidence separately.

The query `synthetic stress symbol` lexically matches every row and returns 20 ranked matches. The harness fails unless `total` equals the full corpus size and `truncated` is exact.

| Files/chunks | Populate seconds | Query ms | Database MiB | Go heap MiB | Exact total |
|---:|---:|---:|---:|---:|---:|
| 1,000 | 0.283 | 46.512 | 4.29 | 16.83 | 1,000 |
| 10,000 | 1.239 | 159.407 | 41.96 | 23.75 | 10,000 |
| 100,000 | 9.525 | 1,933.071 | 420.01 | 14.94 | 100,000 |
| 1,000,000 | 154.480 | 24,935.293 | 4,202.88 | 21.42 | 1,000,000 |

The database grows linearly while the response working set does not: one million rows remained at 21.42 MiB of measured Go heap because vector and lexical scans retain bounded top-K heaps. Query cost remains linear and reaches 24.94 seconds at the upper stress target. That measurement caused the host's default semantic deadline to become two minutes instead of sharing the 30-second exact-search deadline; an explicit caller deadline remains authoritative. This is an honest upper-bound cost, not a sub-second support claim, and the adapter boundary in ADR-0020 remains the place to replace exact vector ranking if a future product target requires lower latency.

## Exact-search adapter

The exact corpus uses ordinary 41-byte Go files distributed across directories of 10,000 entries. Exactly one file contains `UniqueReleaseTarget`. The production inventory hashes the bind-mounted files, the production Zoekt adapter writes its index to a Docker volume, and the harness fails unless every file is published and the literal query returns exactly one untruncated match.

Hardlinks were tried during harness development and rejected: concurrent link creation across the Docker Desktop bind returned `invalid argument`, and hardlinks would not represent the independently saved paths LCTK must inventory anyway.

| Files | Index seconds | Query ms | Index MiB | Go heap MiB | Published files |
|---:|---:|---:|---:|---:|---:|
| 1,000 | 5.842 | 2.091 | 0.26 | 60.45 | 1,000 |
| 10,000 | 50.884 | 13.488 | 2.45 | 67.54 | 10,000 |
| 100,000 | 490.808 | 150.968 | 24.39 | 167.13 | 100,000 |
| 1,000,000 | 4,238.336 | 1,753.652 | 243.77 | 784.50 | 1,000,000 |

Index size remained linear through one million ordinary files. The initial one-million-file build took 70.64 minutes across Docker Desktop's Windows file-sharing boundary, while the exact literal query completed in 1.75 seconds with one match and no truncation. The reported heap includes the live Zoekt index after publication, not only transient query allocations. These results establish a measured upper curve, not a promise that a heterogeneous million-file repository will have the same latency or memory profile.

## Reproduction

```powershell
./scripts/stress.ps1 -Adapter semantic -Counts '1000,10000,100000,1000000'
./scripts/stress.ps1 -Adapter exact -Counts '1000,10000,100000,1000000'
```

The one-million-file exact fixture is intentionally expensive to create on Docker Desktop file sharing. The product accepts multi-hour initial indexing only when progress remains observable and already-ready capabilities remain available; this suite measures the initial build without turning that upper target into a normal-repository promise.
