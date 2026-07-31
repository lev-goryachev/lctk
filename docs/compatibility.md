# Compatibility

## Initial targets

| Component | Target contract | Current evidence | Not yet proven |
|---|---|---|---|
| Windows host | Windows 10 22H2, x86-64 | Go build and tests on `windows-latest`; Windows amd64 archive construction | Execution on Windows 10, service installation, NTFS edge cases, Docker Desktop lifecycle and file sharing |
| macOS host | macOS 13, arm64 | Go build and tests on `macos-15`; Darwin arm64 archive construction | Execution on macOS 13, launch service, APFS edge cases, Docker Desktop lifecycle and file sharing, notarization |
| Docker Desktop | Current supported line at release time, in **Linux container mode** | Moby API diagnostic reports daemon availability; Slice 1.2 drove real container lifecycle, Compose generation, per-project network and volume isolation, read-only source mounts, and volume persistence across stop/start against Docker Desktop 29.5.3 with Compose v5.1.4 on Windows 10 | macOS, other Docker Desktop versions, resource behavior under load, and multi-project scale |

LCTK project stacks are Linux containers, because [ADR-0011](adr/0011-zoekt-exact-search-backend.md) requires a Linux boundary for the search backend. A Windows host running Docker in Windows container mode answers every query and then rejects a Linux image with an opaque manifest error, so LCTK checks the runtime's reported OS up front and returns a typed error naming the fix.
| MCP | Streamable HTTP through official Go SDK v1.6.1 | In-process client/server transport test calls `foundation_info`; Slice 0.2 harness verifies route/grant/reconnect behavior against candidate gateways | Codex extension integration and persistent production tools |
| Codex client | Streamable HTTP, MCP protocol `2025-06-18`, with `url` plus `bearer_token_env_var` | Slice 0.4 drove the real `codex-cli 0.146.0-alpha.9.2` from extension `26.727.40816` against a route-bound endpoint on Windows 10: full handshake, `tools/list`, `tools/call`, typed error, reload reconnect, and refusal of a foreign project token | macOS, the extension UI itself, non-alpha Codex versions, timeout enforcement, repeated or concurrent sessions, and the experimental app-server protocol remaining stable |

The initial resource goal is a 16 GB RAM, CPU-only machine. No project-count, latency, or million-file performance guarantee exists yet.

## Meaning of evidence

- **Build evidence** means source compiles for an environment.
- **Hosted test evidence** means automated behavior ran on the named GitHub runner image.
- **Artifact construction evidence** means an archive was created; it does not prove extraction or execution.
- **Local measured evidence** means a tracked harness drove a real third-party artifact on one maintainer machine and recorded the result. It is stronger than documentation and weaker than hosted test evidence, because it is not repeated automatically.
- **Certified** requires repeatable tests on the exact target contract, including Docker Desktop where relevant.

Current configurations are targets, not certified support claims. See [ADR-0008](adr/0008-platform-and-ci-baseline.md).

## Certification gates

Before the first supported release, LCTK must add:

1. extracted-archive smoke tests on both target architectures;
2. target-minimum operating-system execution;
3. Docker Desktop project-mount, isolation, stop/start, and persistence tests;
4. filesystem rename, coalescing, overflow, downtime, and reconciliation tests;
5. installer/service, upgrade, rollback, signing, and macOS notarization verification;
6. baseline resource and stress measurements.
