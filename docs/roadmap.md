# Roadmap

## Delivery policy

Work proceeds in small, verifiable vertical slices. Each slice:

- belongs to the target architecture rather than being a disposable demonstration;
- has an explicit user scenario;
- includes automated tests for the relevant boundary;
- updates the documentation and ADRs;
- is not claimed to work without reproducible verification.

Codebase Memory MCP may be studied as architectural prior art, but it is not a roadmap backend or integration candidate. [ADR-0010](adr/0010-codebase-memory-mcp-reference-only.md) and the [comparative assessment](spikes/codebase-memory-mcp-assessment.md) record that boundary. Advanced graph, semantic, and repository-map work remains LCTK-owned and must be delivered through the same slice and verification policy.

## Stage 0 — Research and contracts

### Slice 0.1: Repository foundation

**Status:** complete as a public pre-alpha repository and executable foundation.

Delivered:

- Apache-2.0 repository, documentation, policy, and ADR baseline;
- Go 1.25 module and one `lctk` executable;
- official MCP Go SDK v1.6.1 Streamable HTTP compatibility tool;
- health endpoint, `fsnotify` watcher proof, and read-only Moby Docker diagnostic;
- automated tests and hosted Windows/macOS CI;
- non-publishing Windows amd64 and Darwin arm64 dry-run archives plus checksums.

Verification: clean-checkout checks run on hosted Windows and macOS runner environments. The workflows provide build, test, and artifact-construction evidence but do not certify Windows 10 22H2, macOS 13, Docker Desktop behavior, or native archive execution on those targets.

The current `/mcp` endpoint and `foundation_info` tool are foundation compatibility evidence only. They do not implement the ADR-0009 project-scoped gateway.

### Slice 0.2: MCP/gateway spike

The reproducible scenario, hard gates, measurements, and scoring rules are defined in the [gateway evaluation contract](spikes/gateway-evaluation.md).

**Status:** complete; the [measured results](spikes/gateway-evaluation-results.md) support the accepted LCTK-owned Go gateway in [ADR-0009](adr/0009-embedded-go-gateway-and-project-runtime.md).

Evaluated ContextForge, Docker MCP Gateway, MCPJungle, and a minimal custom gateway against the same scenario:

- Streamable HTTP;
- virtual `/projects/{id}/mcp` routes;
- server-side context injection;
- dynamic project registration;
- per-client and per-project tool filtering;
- typed errors;
- local Docker deployment;
- license, maintenance, protocol compliance, and overhead.

Outcome: ADR-0009 selects an LCTK-owned Go gateway embedded in the host daemon. Spike code remains evidence and does not become a production dependency.

### Slice 0.3: Search backend spike

**Status:** complete; the [measured results](spikes/search-backend-evaluation-results.md) support the accepted Zoekt backend in [ADR-0011](adr/0011-zoekt-exact-search-backend.md).

The [evaluation contract](spikes/search-backend-evaluation.md) and [measured results](spikes/search-backend-evaluation-results.md) compare Zoekt, Livegrep, and OpenGrok. `ripgrep` is the correctness oracle and diagnostic baseline, not a persistent backend.

Verify:

- incremental indexing;
- Windows and macOS paths through mounted `/workspace`;
- regex, literal, glob, and language filters;
- update/delete/rename;
- persistent restart;
- latency and disk use;
- license and arm64 images.

Outcome: ADR-0011 selects pinned Zoekt behind a narrow LCTK-owned working-tree adapter. Livegrep and OpenGrok fail required incremental/correctness or packaging hard gates. Spike code remains evidence and does not become production code.

### Slice 0.4: Codex compatibility spike

**Status:** complete; the [measured results](spikes/codex-compatibility-results.md) support the accepted integration contract in [ADR-0012](adr/0012-codex-integration-contract.md). All six hard gates of the [verification contract](spikes/codex-compatibility.md) pass against Codex extension `26.727.40816` and bundled `codex-cli 0.146.0-alpha.9.2`.

Using the current official documentation and the actual extension, verify:

- project-local config path/schema;
- Streamable HTTP fields;
- bearer token support;
- environment and key-helper capabilities;
- required server behavior;
- reload and reconnect UX.

Measured against Codex extension `26.727.40816` and bundled `codex-cli 0.146.0-alpha.9.2`:

- project-local `.codex/config.toml` works, but only in a trusted project, and it overrides same-named user-global entries;
- Streamable HTTP is first class, with `url`, `bearer_token_env_var`, `http_headers`, `env_http_headers`, `enabled`, and timeout fields;
- an inline `bearer_token` is rejected, so a token must come from an environment variable or OAuth, and no key-helper mechanism exists;
- `codex doctor --json` gives a local reachability and credential diagnostic with no model turn, and its reachability probe is unauthenticated, so a typed `401` still counts as reachable;
- `config/mcpServer/reload` is the reload mechanism, and it performs a full reconnect with a new session rather than an in-place refresh;
- the client speaks MCP protocol version `2025-06-18`, honors the server-issued session id, and works against a stateless JSON endpoint, so server-initiated streaming is not required;
- a model-supplied `project_id` does not change server-enforced scope, and a token issued for one project is refused on another project's route.

Outcome: a verified integration contract, not an assumption. Credential delivery to the editor environment is the remaining design problem, not protocol compatibility.

## Stage 1 — First end-to-end lifecycle

### Slice 1.1: Local registry

**Status:** complete. Persistence is recorded in [ADR-0013](adr/0013-registry-persistence.md).

Implemented:

- a typed project model and migrations, stored as a versioned JSON document in a per-user LCTK home with atomic replacement, refusal to reset on corruption, and a typed error for a newer schema;
- native path canonicalization: absolute resolution, symlinks, junctions, macOS firmlinks, Windows 8.3 short-name expansion, drive-letter case, and a measured rather than assumed case-sensitivity probe;
- a stable local project ID derived from the canonical path, identical for every alias of one folder and constrained to a charset safe in a route segment and a container name;
- `lctk project add/status/remove`, which start no containers, networks, volumes, or indexing;
- a manifest parser for `.mcp-project.yaml` with an untracked `.mcp-project.local.yaml` override, strict validation, warnings for unknown fields, and rejection of any attempt to declare a path, mount, grant, secret, or capability.

Verified:

- different Windows drives produce distinct identities;
- Windows and macOS path handling through hosted CI on both platforms;
- duplicate, case, and path-alias handling, with `os.SameFile` as the authority and the comparison key as the fallback for an unavailable volume;
- the manifest cannot replace the authoritative host path, both because such a declaration is rejected and because the manifest type has no field able to hold one.

### Slice 1.2: Reproducible project container

Implement:

- deterministic Compose generation;
- reusable versioned demo image;
- a project network and persistent volume;
- `start/stop/restart`;
- health and typed lifecycle state.

Tests:

- two projects receive separate mounts, networks, and volumes;
- stop/start preserves marker and index state;
- generated configuration is reproducible.

### Slice 1.3: Project-scoped MCP `project_info`

Implement:

- shared localhost gateway;
- route `/projects/{project_id}/mcp`;
- automatic project grant;
- one tool, `project_info`;
- `PROJECT_NOT_FOUND`, `PROJECT_STOPPED`, `PROJECT_STARTING`;
- request and project IDs in local logs.

Tests:

- the credential and route must agree;
- a different model-supplied `project_id` does not change scope;
- a stopped project returns a typed error.

### Slice 1.4: Persistent `exact_search`

Connect the selected indexed backend through a custom stable adapter and API:

- literal and regex;
- path glob;
- snippets, pagination, and limits;
- project-relative normalized paths;
- provenance and index generation;
- persistent restart.

Tests:

- results come only from the mounted project;
- update/delete/rename;
- restart uses the saved index and performs catch-up;
- malicious absolute or relative paths do not expand scope.

### Slice 1.5: Actual Codex end-to-end

LCTK generates local Codex configuration and a grant. The following is verified through the Codex extension in VS Code:

```text
register folder
→ start stack
→ connect project endpoint
→ project_info
→ exact_search
→ attempt cross-project access and receive refusal/no data
→ stop and receive typed error
→ restart and reuse persistent index
```

This is the first required vertical slice from the original brief.

After this end-to-end boundary is reproducibly verified, open the documented external pull-request intake process.

## Stage 2 — Watcher and freshness

### Slice 2.1: Host change journal

- native watcher adapters;
- normalized events;
- configurable debounce;
- persistent checkpoint;
- on-demand wakeup and idle activity.

### Slice 2.2: Incremental exact index

- batch update;
- bulk-change detection;
- reconciliation after downtime or overflow;
- freshness metadata in the tool response.

### Slice 2.3: Resource policies and Admin UI baseline

- quiet, normal, and fast background modes;
- disk and resource estimates and confirmation;
- add, start, stop, status, and index progress;
- logs/doctor;
- client grants.

## Stage 3 — Safe coding operations

- project-relative file-read helpers only where they complement client capabilities;
- Git status/diff/changed files;
- a separate runner;
- per-project network setting of `none` or `full`;
- typed command policy generated through validated manifest;
- timeout, cancellation, and output and resource limits;
- tests at the project boundary.

LCTK does not duplicate built-in VS Code or Codex file-editing tools without adding value.

## Stage 4 — Symbol and AST intelligence

Add language adapters in small, independent slices without fixing their order in advance:

- TypeScript/JavaScript;
- Python;
- Rust;
- C/C++.

For each language, separately verify symbols, definitions, references, diagnostics, lifecycle, and resource behavior. Where possible, the Tree-sitter/AST layer provides file outlines and chunks independently of a specific LSP.

## Stage 5 — Persistent semantic intelligence

- AST-aware chunk model;
- a local CPU embedding model;
- replaceable vector adapter;
- shared inference compute, isolated project collections;
- incremental invalidation;
- hybrid lexical and vector ranking;
- freshness and commit awareness.

## Stage 6 — Graph, repository map, and memory

- persistent code graph adapter;
- callers/callees/dependency paths/impact;
- compact repository map;
- explicit project-memory CRUD and decision records;
- provenance, confidence, and review metadata.

## Stage 7 — Hardening and public release

- installer/bootstrap command;
- explicit `lctk update` with a migration plan and rollback;
- signed and notarized artifacts/images;
- generated dependency attribution, SBOM, and provenance;
- production release automation and support policy;
- target-hardware and Docker Desktop certification matrix;
- performance stress suite up to the upper target of one million files;
- Linux roadmap based on measured portability gaps.
