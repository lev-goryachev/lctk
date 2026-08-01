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

**Status:** complete. Resource naming is specified in [project lifecycle](project-lifecycle.md#compose-resource-naming), closing the [ADR-0003](adr/0003-reusable-images-and-project-stacks.md) follow-up.

Implemented:

- deterministic Compose generation, byte-reproducible and stored under the per-user LCTK home rather than in the repository, using long mount syntax so a Windows drive letter is unambiguous;
- a reusable versioned image built from a digest-pinned base, shared by every project, tagged with the product version per [ADR-0007](adr/0007-unified-versioning.md), plus `lctk image build/status`;
- a per-project network and persistent volume, with the source mounted read-only at `/workspace` and project state at `/var/lib/lctk`;
- `lctk project start/stop/restart`, where stop and remove release runtime resources but never delete the project volume;
- typed lifecycle state — `stopped`, `starting`, `running`, `error`, `unknown` — with container health, a one-line explanation, and an explicit `retryable` flag, and with an unreachable container runtime distinguished from a failed project so registry information stays usable while Docker Desktop is closed.

Verified:

- two projects receive separate mounts, networks, and volumes, confirmed in the runtime and by each container being unable to read the other's source;
- stop/start preserves marker state in the project volume, confirmed by a start counter and a creation timestamp surviving a full stop;
- generated configuration is reproducible, byte-identical across a restart;
- the source mount is genuinely read-only from inside the container.

Container-dependent verification runs against real Docker on a developer machine and skips explicitly on hosted runners, which have no usable Linux Docker daemon. It is never simulated. See [compatibility](compatibility.md).

### Slice 1.3: Project-scoped MCP `project_info`

**Status:** complete. This is the first slice an agent can actually connect to.

Implemented:

- the shared localhost gateway inside the host daemon, serving `/projects/{project_id}/mcp`, with the registry and grants read per request so a project registered while the daemon runs becomes reachable without a restart;
- an automatic project grant issued on registration, stored owner-only outside any repository, revoked when its only project is removed, and surfaced through `lctk grant show/list/revoke` with the token withheld unless `--reveal` is given;
- one tool, `project_info`, answering from the route and the server-side registry, and reporting `scope_source` so a caller can verify that its own arguments did not influence the answer;
- typed errors `PROJECT_NOT_FOUND`, `PROJECT_STOPPED`, `PROJECT_STARTING`, `SERVICE_UNAVAILABLE`, `AUTH_REQUIRED`, `AUTH_FORBIDDEN`, `RUNTIME_UNAVAILABLE`, and `RUNTIME_UNSUITABLE`, each carrying `retryable`, `recommended_action`, `project_id`, and `request_id`;
- request, project, grant, and client identifiers in local logs, with the token never written.

Verified:

- the credential and route must agree: a token issued for one project is refused on another with `AUTH_FORBIDDEN`, while each token still works on its own route;
- a model-supplied `project_id`, `repository_root`, or `path` does not change scope;
- a stopped project returns a typed `PROJECT_STOPPED` rather than empty data, and a starting one returns a retryable `PROJECT_STARTING`;
- an unauthenticated caller cannot learn whether a project exists, because the credential is checked before the registry;
- the host path is never exposed to the client; `project_info` reports the in-container workspace.

Confirmed by hand against a running daemon with a real container: `initialize` succeeded on the routed project, a foreign token was refused with 403, a `project_info` call carrying a deliberately wrong `project_id` still answered for the routed project, and a stopped project returned the full typed error envelope with 503.

### Slice 1.4: Actual Codex end-to-end

**Status:** the chain is measured and passing against real components; the extension user interface is not yet exercised, so the slice is not claimed complete. The [measured results](spikes/codex-end-to-end-results.md) state both.

LCTK generates local Codex configuration and delivers the project grant. The scenario:

```text
register folder
→ start stack
→ connect project endpoint
→ project_info
→ attempt cross-project access and receive refusal/no data
→ stop and receive typed error
→ restart and reconnect
```

Credential delivery is decided in [ADR-0014](adr/0014-project-credential-delivery.md), which closes the item [ADR-0012](adr/0012-codex-integration-contract.md) named as blocking this slice.

Implemented:

- generation of the Codex `mcp_servers` entry for a project endpoint, written into a marker-delimited region of the user's own configuration file, leaving every other byte untouched, previewed by default and written only with `--apply`, backed up before each write, and parsed before it is written so a generated document cannot break the client's whole configuration load;
- refusal to overwrite a same-named entry LCTK did not generate unless forced, and outright refusal to rewrite one written as an inline key;
- credential delivery through a process LCTK starts, so the token exists only in that process's environment, no generated file holds a secret, and LCTK makes no durable change to the machine; the persistent alternative is printed for the operator to run, never applied;
- `lctk codex status/config/env/launch`, with the token withheld unless explicitly revealed;
- a typed `AUTH_REQUIRED` on an unauthenticated probe of any project route, whose recommended action names the case where a client started before its grant existed, worded identically for a project that does not exist.

Verified:

- the credential reaches a started process's environment, and nothing is left behind in LCTK's own;
- generating an entry never writes a token, and re-applying an unchanged entry rewrites nothing;
- writing preserves unrelated servers, tables, and quoted path keys in a file LCTK does not own;
- an unauthenticated `GET`, `HEAD`, or `POST` returns the same typed `401` for a registered project and an invented one.

Measured end to end against the real `lctk` executable, a real daemon, real containers, and the Codex binary the VS Code extension runs: register, start, connect, `project_info` with a deliberately wrong `project_id` argument, `403 AUTH_FORBIDDEN` for a foreign token, a typed `PROJECT_STOPPED` that survives into the client's own error text, project state preserved across a stop, and reconnection after a restart with no configuration change.

Remaining before the slice is claimed complete: exercise the extension user interface, which the harness cannot drive. The manual steps are listed in the results document.

This slice originally followed persistent search. It was moved ahead of it so that the client boundary is proven against a real editor before the heaviest backend work begins, and so that search lands inside a chain already known to work end to end. The search steps of the original scenario move to Slice 1.5, which re-runs this chain with `exact_search` in it.

### Slice 1.5: Persistent `exact_search`

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

The Slice 1.4 chain is then re-verified through the Codex extension with `exact_search` and index reuse across a restart included:

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
