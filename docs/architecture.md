# LCTK architecture

## Status

Architecture baseline. Go is selected for LCTK-owned code, and the shared MCP gateway is an LCTK-owned component embedded in the host daemon. The persistent exact-search engine is Zoekt behind an LCTK adapter under [ADR-0011](adr/0011-zoekt-exact-search-backend.md), and the local registry is a versioned JSON document under [ADR-0013](adr/0013-registry-persistence.md). The change journal is a versioned per-project document in the LCTK home under [ADR-0015](adr/0015-change-observation-is-complete-or-declared-incomplete.md). Storage for index metadata and for semantic and graph state remains open. Codebase Memory MCP is reference-only prior art under [ADR-0010](adr/0010-codebase-memory-mcp-reference-only.md); it is not an LCTK core, backend, wrapper, or production dependency.

## Current implementation

The current implementation is deliberately narrower than the target architecture:

- one Go `lctk` executable with CLI and foreground daemon command families;
- a standard-library HTTP daemon with `GET /health`;
- the official MCP Go SDK Streamable HTTP handler at `/mcp` with temporary tool `foundation_info`;
- an `fsnotify` basic event-delivery proof;
- a read-only Moby API diagnostic for Docker Desktop availability;
- the local project registry from Slice 1.1: canonical host paths, stable project identities, `lctk project add/status/remove`, and manifest parsing, none of which start a service;
- the per-project container stack from Slice 1.2: deterministic Compose generation, a reusable versioned image, an isolated network and persistent volume, a read-only source mount, `lctk project start/stop/restart`, and typed lifecycle state with health;
- the project-scoped MCP endpoint from Slice 1.3: `/projects/{project_id}/mcp` inside the host daemon, automatic per-project grants, the `project_info` tool, typed lifecycle and authorization errors, and request-correlated local logs;
- the client integration from Slice 1.4: generated Codex configuration written into a marker-delimited region of the user's own file, credential delivery through a process LCTK starts, and `lctk codex status/config/env/launch`;
- persistent exact search from Slice 1.5: a per-project search service in the project container, a staged generation store published atomically, the project's own ignore rules honoured, and the `exact_search` tool behind a stable host-side adapter;
- the host change journal from Slice 2.1: a native filesystem watcher per running project, normalized project-relative events, a configurable debounce, a persistent per-project journal that is either complete since its checkpoint or explicitly incomplete, and freshness reported through `project_info`;
- the constrained runner from Slice 3.2: `run_command` executing only what the machine owner approved, one container per run with the project mounted writable and everything else denied, and an append-only audit record;
- Git awareness from Slice 3.1: `git_status` and `git_diff` on the project route, read-only and route-scoped, plus the branch, commit, and dirty state in `project_info`;
- resource policy and the admin surface from Slice 2.3: background-load modes that change what a project costs, disk reporting with a refusal to start on a nearly full volume, and a local admin page over an API a project credential cannot reach;
- incremental indexing from Slice 2.2: a settled batch applied to the index automatically, a gap reconciled instead of applied, a removed directory retracting everything beneath it, bulk changes rebuilt rather than applied, and a search that flushes pending changes before answering so an edit made a moment ago is already searchable.

`lctk project reindex` remains for explicit catch-up and for recovering a corrupt index, but it is no longer how the index keeps up with editing. The legacy `/mcp` endpoint remains foundation compatibility evidence only and is not project-scoped.

Git runs on the host rather than in the project container. The container mounts the source read-only and Git wants to refresh its index when asked for status, and the host is where the user's Git configuration lives, so an answer computed elsewhere would be about a different repository than the one they see. A project registered below a repository root is scoped to its own subtree, so a project endpoint cannot report a sibling directory's changes.

The watcher derives what it observes from the project's own service rather than reading ignore files itself, so the exclusion policy has exactly one implementation. On this repository that is 42 watched directories out of 56,077 on disk.

The search engine runs only inside the project container. It is built as a separate Go module, which keeps it out of the portable host executable by construction rather than by convention, as [ADR-0011](adr/0011-zoekt-exact-search-backend.md) requires. The host reaches it on a loopback port the container runtime assigns, so no port allocation is coordinated and no project service is reachable from the network.

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

As implemented in Slice 2.3, it is served at `/admin` on the daemon's loopback listener: one embedded HTML page with no build step and no remote asset, over a JSON API that lists projects with their state, index, and change record; starts, stops, restarts, and reindexes them; sets a project's resource mode; lists and revokes client grants; and shows the container-runtime diagnostic and the daemon's recent log. Grant tokens are never served to it. The session is decided in [ADR-0016](adr/0016-admin-surface-and-local-session.md).

## Runtime modes

The user selects one global machine mode:

- `always-on` — selected stacks are kept running;
- `on-demand` — the daemon starts a stack on an MCP request or meaningful project activity and stops it after a configurable idle timeout.

The first on-demand call may remain open until the stack is ready, subject to a separate startup timeout. The client must receive progress or status rather than an ambiguous `connection refused`.
