# Search backend evaluation harness

This directory contains the tracked, evidence-only harness for Slice 0.3. It is not production LCTK code and does not define the public `exact_search` schema.

## Pins

- Go: 1.25.9
- Zoekt: `2b2ce2e398e6bee68d67143f567b6c6199340c7f`
- build image: `golang:1.25.9-bookworm` at `sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3`
- ripgrep oracle: 15.2.0; the platform archive and published checksum must be verified before use

## Commands

`search-eval` provides:

- `fixture`: create deterministic `alpha` and `beta` saved working trees;
- `full`: build a persistent Zoekt base shard and LCTK-owned hash manifest;
- `apply`: apply targeted create, modify, delete, or rename changes as delta shards;
- `reconcile`: compare the saved tree with the manifest and apply offline changes;
- `query`: execute a normalized query and optionally compare membership with ripgrep JSON output;
- `bench`: run at least 100 sequential searches through one open search session;
- `stats`: report included source and persistent-index sizes.

Run `go test -count=1 ./...` from this directory in Linux. Zoekt's low-level indexing package does not compile natively on Windows, so the tracked Dockerfile is the reproducible execution boundary:

1. build the image for `linux/amd64` or `linux/arm64` using this directory as build context;
2. mount an ignored repository-local evidence directory at `/evidence`;
3. create fixtures under `/evidence/fixtures`;
4. keep indexes under `/evidence/indexes`, separate from the source trees;
5. mount the verified ripgrep binary read-only when using `query --oracle`.

The harness refuses to reset a fixture root unless its basename is exactly `fixtures`. It does not require a Docker socket and never writes to a source tree mounted read-only by a production-style runtime.

## Interpretation limits

The adapter deliberately uses Zoekt's unstable low-level `index` package to prove capability. Production adoption requires a narrow LCTK-owned adapter, exact revision pinning, staged generation publication, and an explicit delta compaction/full-rebuild policy. Timings collected through Docker Desktop Windows bind mounts characterize this fixture and machine only.
