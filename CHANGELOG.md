# Changelog

All notable changes to LCTK will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases will follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Initial architecture and product documentation.
- Apache-2.0 `LICENSE`, `NOTICE`, security policy, governance, support, and pre-contribution repository policy.
- Go 1.25 foundation with a single `lctk` executable.
- `GET /health` and `/mcp` using the official MCP Go SDK v1.6.1, with temporary compatibility tool `foundation_info`.
- Native watcher event-delivery proof using `fsnotify v1.10.1`.
- Read-only Docker Desktop availability diagnostic using Moby client v0.5.0.
- Automated CLI, HTTP, MCP transport, and watcher tests.
- Hosted Windows/macOS CI and non-publishing Windows amd64 and Darwin arm64 dry-run archives plus checksums.
- Reproducible Slice 0.2 gateway harness, measurements, and hard-gate results.
- Accepted ADR-0009 selecting an LCTK-owned Go gateway embedded in the host daemon.
- Registered project lifecycle with isolated Linux containers, persistent state volumes, loopback-only published ports, and route-bound bearer grants.
- Persistent Zoekt exact search, native change observation, reconciliation after downtime, resource modes, and typed freshness and recovery status.
- Read-only Git status/diff tools, an approved-command runner with container-enforced limits, and a local Admin API/UI separated from project MCP authority.
- Tree-sitter outlines, definitions, references, and syntax status for Go, Python, Rust, C, C++, JavaScript, TypeScript, and TSX.
- Persistent AST-aware hybrid semantic search backed by one shared pinned local embedding process and an isolated SQLite store per project.
- Derived name-match callers, callees, dependency, impact, and repository-map tools plus explicit reviewed project memory with optimistic revisions.
- Transactional signed-manifest bootstrap, update, schema migration, host activation, and rollback through a stable digest-verifying launcher.
- Fail-closed Authenticode and Apple signing/notarization release gates, native Linux amd64/arm64 image execution, SBOMs, checksums, attestations, and signed release manifests.
- Complete 18-tool verification through Codex and an independent MCP Go SDK client, plus parameterized semantic and exact-search stress evidence through one million files or chunks.

### Fixed

- Exact-only code-intel containers no longer treat a typed nil semantic store as enabled during startup reconciliation.
- Semantic ranking retains bounded top-K candidates while preserving exact total counts and deterministic tie-breaking.
- Exact inventory hashing uses bounded concurrency instead of serial storage round trips.
- Schema migration activation, validation, failure preservation, and rollback now recover every process-interruption state without leaving the authoritative database name absent or trusting a replacement inode.
