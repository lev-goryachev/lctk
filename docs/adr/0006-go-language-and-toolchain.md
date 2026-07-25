# ADR-0006: Go language and toolchain for LCTK-owned code

- Status: accepted
- Date: 2026-07-25
- Deciders: project maintainers

## Context

LCTK needs one cross-platform host executable containing the daemon and CLI, reliable Streamable HTTP MCP support, native filesystem observation, Docker Desktop integration, explicit state handling, and straightforward Windows/macOS delivery. External search, language, graph, and model engines remain separately versioned components and do not need to share the host implementation language.

## Decision

Use Go for LCTK-owned daemon, CLI, control-plane, adapter, and supporting-library code.

- Use Go modules and the Go 1.25 toolchain line.
- Ship one `lctk` executable with command families rather than separate daemon and CLI binaries.
- Use the official MCP Go SDK v1.4.1 for MCP protocol boundaries.
- Use `fsnotify v1.9.0` for the native watcher adapter and Moby client v0.3.0 for Docker API integration.
- Prefer the Go standard library for HTTP, JSON, context, concurrency, signals, and process lifecycle.
- Keep watcher, Docker, gateway, and backend integrations behind LCTK-owned contracts.
- Allow external engines to remain in their native implementation languages and integrate them as pinned binaries or images.

## Rationale

Go provides explicit, testable state and error handling, a stable official MCP SDK, practical native watcher and Docker libraries, fast cross-platform builds, and a single self-contained executable without an additional runtime installation. Its concurrency and standard networking library fit the long-running local daemon and bounded worker model.

## Consequences

### Positive

- One portable executable can contain CLI and daemon behavior.
- The stable official MCP SDK reduces protocol-integration risk.
- Cross-platform compilation and CI are simple.
- Build times and contributor setup remain small enough for frequent verification.

### Negative

- Go runtime and module upgrades require explicit compatibility and dependency review.
- Native watcher, service installation, path, and Docker behavior still require target-system tests; library portability is not certification.
- ML or language-specific engines still require process/image adapters.

### Follow-up

- Introduce LCTK-owned interfaces as Docker, watcher, and gateway behavior grows beyond the Slice 0.1 proofs.
- Add target-system service installation, filesystem semantics, and Docker Desktop tests.
- Review the Go toolchain and pinned direct dependencies before every release line.
