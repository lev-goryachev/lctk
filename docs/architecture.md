# LCTK architecture

## Status

Architecture baseline. Go is selected for LCTK-owned code, and the shared MCP gateway is an LCTK-owned component embedded in the host daemon. The persistent exact-search engine is Zoekt behind an LCTK adapter under [ADR-0011](adr/0011-zoekt-exact-search-backend.md), and the local registry is a versioned JSON document under [ADR-0013](adr/0013-registry-persistence.md). Storage for the change journal, index metadata, and semantic and graph state remains open. Codebase Memory MCP is reference-only prior art under [ADR-0010](adr/0010-codebase-memory-mcp-reference-only.md); it is not an LCTK core, backend, wrapper, or production dependency.

## Current implementation

The current implementation is deliberately narrower than the target architecture:

- one Go `lctk` executable with CLI and foreground daemon command families;
- a standard-library HTTP daemon with `GET /health`;
- the official MCP Go SDK Streamable HTTP handler at `/mcp` with temporary tool `foundation_info`;
- an `fsnotify` basic event-delivery proof;
- a read-only Moby API diagnostic for Docker Desktop availability;
- the local project registry from Slice 1.1: canonical host paths, stable project identities, `lctk project add/status/remove`, and manifest parsing, none of which start a service;
- the per-project container stack from Slice 1.2: deterministic Compose generation, a reusable versioned image, an isolated network and persistent volume, a read-only source mount, `lctk project start/stop/restart`, and typed lifecycle state with health.

There is no project-scoped route, persistent search index, durable change journal, or client grant implementation yet. The current MCP endpoint validates a protocol boundary but is not the future project endpoint, and a running stack carries no code-intelligence capability: the image exists to prove the runtime boundary, and backends arrive in later slices.

## Principles

1. **The IDE is a client, not infrastructure.** VS Code presents the conversation, code, and diffs; heavy indexes live outside the extension.
2. **Scope is assigned by the server.** The authoritative project is determined by the endpoint, credential, and registry, not by model arguments.
3. **One set of artifacts, separate project state.** Images and binaries are reused; mounts, indexes, memory, networks, and volumes belong to a single project.
4. **Stable public tool API.** The client does not know the names of internal search, LSP, vector, or graph engines.
5. **Modular monolith before microservices.** A component becomes a separate service only when a genuine operational or dependency boundary exists.
6. **Persistent and incremental by default.** A restart must not imply a full rebuild.
7. **Local-first.** Core intelligence works after installation without an external backend.
8. **LCTK owns the product boundary.** External libraries and specialized tools may remain replaceable implementation details, but the project registry, grants, public MCP API, lifecycle, policy, orchestration, freshness model, and control plane are LCTK-owned. Complete external code-intelligence products do not become a second core behind an LCTK façade.

## Logical architecture

```text
VS Code + Codex                  Other explicitly granted clients
       │                                      │
       └──────────── Streamable HTTP MCP ─────┘
                              │
              127.0.0.1 embedded LCTK gateway
                    auth, routing, tool policy
                              │ project_id
             ┌────────────────┴────────────────┐
             │                                 │
       Project A stack                   Project B stack
       code-intel + runner               code-intel + runner
       isolated indexes                  isolated indexes
       isolated memory                   isolated memory
             │                                 │
       bind mount A                      bind mount B

Host-side LCTK daemon
├── local registry and secrets
├── shared MCP gateway and grant enforcement
├── host path canonicalization
├── Docker Desktop lifecycle
├── filesystem watcher and change journal
├── resource planning
├── local Admin UI
└── CLI API for lctk
```

## Host-side daemon

A small daemon is installed on Windows and macOS and can optionally start when the user signs in. It is the only LCTK component that:

- registers arbitrary host paths;
- canonicalizes paths using host OS facilities;
- manages the Docker Desktop/Compose lifecycle;
- stores the local registry and client grants;
- serves the shared project-scoped MCP gateway;
- watches for file changes;
- serves the Admin UI and `lctk` CLI;
- starts on-demand stacks and enforces the idle policy.

The embedded gateway does not expose the daemon's Docker client or administrative handlers to coding MCP requests. Project services do not receive the Docker socket. Coding MCP tools do not control the daemon or Docker directly.

## Shared control plane

The shared control plane is an LCTK-owned Go component inside the host daemon, as accepted in [ADR-0009](adr/0009-embedded-go-gateway-and-project-runtime.md). It continuously provides a stable localhost endpoint. Its responsibilities are:

- Streamable HTTP MCP transport;
- authentication and client grants;
- extracting `project_id` from server-side route context;
- capability filtering;
- virtual project servers;
- schema and path normalization;
- timeouts and health aggregation;
- typed errors;
- local audit logs and trace context.

Accepted public route:

```text
http://127.0.0.1:4444/projects/{project_id}/mcp
```

The gateway uses the official MCP Go SDK and LCTK-owned registry, grant-validation, health-resolution, error-translation, and upstream-transport interfaces. Compatibility of route, authentication, and tool contracts does not depend on an external gateway product.

## Project runtime

The target model is a separate Docker Compose project or equivalent namespace for each registered folder. Two runtime boundaries are sufficient for the first working version:

- **code-intel** — read-only source mount, adapters, and persistent indexes;
- **runner** — writable source mount and project-command execution.

Heavyweight or incompatible backends can later be moved into separate services without changing the external MCP API.

Project-specific state:

- bind mount source root;
- exact/symbol/AST/semantic/graph indexes;
- project memory;
- change/index manifests;
- runtime caches;
- Docker network and volumes;
- health and freshness state.

## Code-intel boundary

Client tools describe a user action:

```text
exact_search
code_search_semantic
symbol_find
symbol_definition
symbol_references
file_outline
repository_map
callers_find
callees_find
impact_analyze
diagnostics_get
```

`code-intel` selects adapters, merges results, and removes duplicates. Every relevant response includes compact:

- normalized project-relative paths;
- provenance;
- freshness/index generation;
- source commit and dirty state, where applicable;
- bounded snippets and a pagination cursor.

## Client grants

Access to the localhost MCP endpoint is not globally open. LCTK creates a separate grant for each client:

- client name;
- permitted project IDs;
- capability profile (`read`, `code`, and a separate `admin` profile);
- expiration;
- revoke/rotate state;
- last-used metadata.

Codex receives a grant automatically when project-local configuration is generated. The user adds any other CLI or desktop client through the Admin UI or CLI. A browser origin receives a separate short-lived session only after explicit confirmation of the domain, project, and permissions.

## Admin UI

The minimal local web UI must support:

- add/remove project;
- start/stop/restart;
- index progress and freshness;
- logs/doctor;
- runner network, resource mode, and capability policies;
- client grants and revocation.

The Admin API is not exposed through the regular project `code` endpoint.

## Runtime modes

The user selects one global machine mode:

- `always-on` — selected stacks are kept running;
- `on-demand` — the daemon starts a stack on an MCP request or meaningful project activity and stops it after a configurable idle timeout.

The first on-demand call may remain open until the stack is ready, subject to a separate startup timeout. The client must receive progress or status rather than an ambiguous `connection refused`.
