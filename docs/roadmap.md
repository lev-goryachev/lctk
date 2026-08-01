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
- official MCP Go SDK v1.7.0 Streamable HTTP compatibility tool;
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

**Status:** complete. The chain is measured and passing against real components, and against two independent MCP client implementations. The [measured results](spikes/codex-end-to-end-results.md) record both runs and what they do not cover.

The acceptance criterion was widened during the slice. It originally read "verified through the Codex extension in VS Code". What the endpoint owes a client is the MCP protocol, not one editor's presentation of it, so a second unrelated client is stronger evidence than a second look at the same one. The slice is accepted on the protocol boundary being verified against two clients, one of them a real agent session. The Codex extension's own panels remain unclicked, which the results document states.

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

Repeated by hand against Claude Code, an unrelated MCP client, on the same live endpoint: handshake and `tools/list`; the credential stored as a variable reference rather than a value and resolved at connect time; a missing variable reported by name; a foreign token refused on another project's route while the correctly paired entry connected in the same check; a typed `PROJECT_STOPPED` for a stopped project; reconnection after a restart; and a real agent session calling `project_info` with a deliberately wrong `project_id` and receiving the routed project, `scope_source: route_and_registry`, and no host path.

This slice originally followed persistent search. It was moved ahead of it so that the client boundary is proven against a real client before the heaviest backend work begins, and so that search lands inside a chain already known to work end to end. The search steps of the original scenario move to Slice 1.5, which re-runs this chain with `exact_search` in it.

### Slice 1.5: Persistent `exact_search`

**Status:** complete. The exclusion policy and the generation store are described in [indexing](indexing.md).

Implemented:

- a per-project search service running in the project's Linux container, built as a separate Go module so the engine cannot enter the portable host executable, which is what [ADR-0011](adr/0011-zoekt-exact-search-backend.md) requires rather than merely asks for;
- a staged generation store: a build writes aside and publishes by replacing one symlink, so a live query sees a whole generation or the previous one and never a half-written index;
- delta builds that hard-link the previous shards, with a bounded delta depth that escalates to a full rebuild, and generation pruning;
- ignore rules from `.gitignore`, then `.lctkignore`, then an untracked `.lctkignore.local`, each honoured in nested directories and each able to re-include what an earlier one excluded, with a short overridable default list and unconditional exclusion of version-control metadata;
- literal and regular-expression search, case sensitivity, path globs, language filters, bounded previews, pagination, and limits;
- an `exact_search` tool on the project route behind [`internal/codeintel`](../internal/codeintel), the stable adapter [ADR-0004](adr/0004-stable-aggregated-tool-api.md) requires, reporting backend, schema version, index generation, index time, and file count as provenance;
- the service published on an ephemeral loopback port assigned by the runtime, discovered from the same inspect call that reports lifecycle state, so many projects run at once with nothing to coordinate;
- `project_info` reporting the search capability and index freshness, and `lctk project reindex` for explicit catch-up and for the documented recovery from a corrupt index.

Verified:

- results come only from the mounted project: a file beside the workspace, and a symbolic link pointing at it, contribute nothing;
- create, modify, delete, and rename, including a rename delivered as one batch;
- a restart answers from the persisted index without rebuilding, and reconciliation catches up changes made while the service was not running;
- a cursor from another index generation is refused rather than silently skipping or repeating results;
- absolute, parent-traversing, and Windows-style paths are refused rather than reinterpreted, in both globs and change batches;
- a corrupt index is a typed error rather than an empty result, and recovers with a rebuild;
- delta escalation loses nothing, and pruning never breaks the published generation;
- a targeted update applies the same ignore rules as a full build, so an ignored file cannot be added by one and dropped by the other.

Measured against this repository through a real container and the live endpoint: about 155 files indexed after ignore rules, literal and regular-expression queries with path globs, pagination walking a result set exactly once, create/rename/delete reflected across successive generations, and an index reused across a restart with the service on a new port the daemon rediscovered without being restarted. A real agent session called `exact_search` through a second MCP client and returned 9 matches with the first at `internal/gateway/gateway.go:286`; `git grep` independently reports the same 9 and the same first location.

A second agent query demonstrated the property that makes filesystem enumeration a requirement rather than a preference. Asked for a regular expression, it returned 8 matches across three files. `git grep` finds only 4 across two, because the third file was saved but not yet committed. A filesystem count agrees with the index exactly.

The ignore policy was not a planned item. It came from running against a real checkout: the first build walked a gitignored local directory of 278,000 cached files and never finished. A fixture would not have found it.

Its second half came from the observation that a version-control ignore file answers "what should not be committed", which is not the question the indexer is asking. `.lctkignore` exists so a deliberately uncommitted local directory can still be searched, and so the project rather than Git decides what is worth indexing.

This closes the [ADR-0011](adr/0011-zoekt-exact-search-backend.md) follow-up to define and test compaction thresholds before persistent search is declared complete. Its remaining follow-ups — watcher-driven scheduling, and published multi-architecture images from a release pipeline — belong to Slice 2.2 and to releasing.

The Slice 1.4 chain now includes search:

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

The search steps of that chain were run live for this slice. The cross-project refusal and the typed stopped-project error were measured in Slice 1.4 and are not changed by this one, because the route resolves the project before a tool is reached; that scope holds for `exact_search` specifically is covered by an automated test, which also asserts that a foreign project's service is never contacted.

This is the first required vertical slice from the original brief.

After this end-to-end boundary is reproducibly verified, open the documented external pull-request intake process.

## Stage 2 — Watcher and freshness

### Slice 2.1: Host change journal

**Status:** complete. The honesty rule the slice is built around is recorded in [ADR-0015](adr/0015-change-observation-is-complete-or-declared-incomplete.md), and it closes the [ADR-0005](adr/0005-host-watcher-and-incremental-indexing.md) follow-ups on normalization, coalescing, overflow, and debounce configuration.

The scenario: a developer edits files in an ordinary editor, and LCTK knows what changed without being told, or says plainly that it does not.

Implemented:

- a host watcher over native OS events, with recursion, coalescing, and normalization owned by LCTK rather than by the library: a project-relative path with forward slashes, a rename delivered as a removal of the old path and a write of the new one, a removed directory reported as a directory because once it is gone the filesystem can no longer say what it was, and a newly created directory adopted together with whatever a tool already wrote into it before a watch could be placed;
- a per-project change journal that makes one claim — every change since its checkpoint is in the pending list, or a gap says otherwise — deduplicated by path, atomically written, and versioned;
- a gap for every condition under which observation could have been incomplete, latched at the earliest reason, cleared only by a consumer that can prove it is closing the gap it set out to close rather than one that opened while it worked;
- the project service as the single authority on what belongs to the project, so the watcher derives its directory list from the same code that decides what is indexed instead of holding a second copy of the ignore engine;
- a watch budget that degrades to a capacity gap and reconciliation, rather than to failure or to exhausting the process's handles;
- configurable debounce, accepted at 3 seconds with a 30-second ceiling, as a machine default in the host settings file and a project proposal in the manifest that the host clamps;
- observation that follows use: a watcher is started for a running project, woken by a request to its route, and released when the project stops or goes idle, with the lapse recorded;
- freshness in the tool response — `project_info` reports a `changes` block and an `index.freshness` verdict that is never optimistic, so a project nothing is watching reports `unknown` rather than `fresh`;
- `lctk project watch`, which reads the journal from disk so it answers when no daemon is running, with `--follow` to stream events for diagnosis, and `lctk settings show`.

Verified:

- a burst of saves to one path is one pending change, and a repeated save does not lose the newest observation;
- a directory removal is distinguishable from a file removal, and a directory is never reported as written;
- version-control metadata produces no events at all;
- a gap that opens during a reconciliation is not cleared by it, which is the case that would otherwise declare an index current about changes nobody looked at;
- an oversized change set becomes a bulk gap rather than a pending list too large to be worth applying;
- an unreadable journal, or one written for another project, is reset with the reset visible as a gap rather than treated as fatal;
- a refused request wakes nothing, so observation cannot be provoked by an unauthenticated caller;
- the tests run clean under the race detector.

Measured against this repository through a real daemon on Windows 10: 42 watched directories out of 56,077 on disk, a save settled 3.0 seconds after the write against a 3-second window, 62 raw observations of one path collapsed into one pending change, a deletion recorded as such, the pending list surviving a hard kill of the daemon, the reload recording a fresh `observation_started` gap while the sequence counter continued, and a deliberately reduced watch budget producing exactly one capacity gap and ten registered directories.

Two defects surfaced only on that live run, neither reachable from a fixture. A quiet project had no journal on disk at all, because the document was written on settle and a project nobody had touched never settled — so `lctk project watch` reported "never observed" about a project being observed at that moment. And Windows reports a write against the containing directory as well as the file, so every save produced two pending changes, the second for a path the indexer is certain to discard.

The journal has no consumer until Slice 2.2, so every project currently reports an incomplete record. That is the accurate reading rather than a defect: nothing has yet brought an index up to date from the journal.

### Slice 2.2: Incremental exact index

**Status:** complete. This is the slice that makes the journal do something: search follows edits with nothing asked of the user.

The scenario: a developer saves a file and an agent searches for what was just written, without any reindex command and without knowing a watcher exists.

Implemented:

- a settled batch applied to the project index automatically, with the journal deciding the path — a complete record is applied as a delta, an incomplete one is reconciled, and the second is never mistaken for the first;
- a checkpoint that advances only on success, carrying the index generation it produced, so a failed update leaves the work outstanding and retries at the next settle instead of being lost;
- subtree deletion: a removed directory retracts everything beneath it, because once it is gone nothing can enumerate what was in it;
- bulk detection on batch size as well as on delta depth, at a quarter of the index with a floor of 500 files, so a branch checkout is rebuilt rather than applied one tombstone at a time;
- reconciliation after downtime, which is what closes the gap every daemon start records;
- a search that flushes pending changes before answering, bounded so that giving up on the wait degrades to a reported-stale answer rather than a hung call or a cancelled rebuild;
- freshness in the tool response: `exact_search` carries the same verdict `project_info` does, and reports what is outstanding whenever the answer is not current.

Verified:

- an incomplete record is reconciled and never applied as a batch;
- a created directory is not sent to the index, and a removed one is sent as a subtree;
- a failed update does not advance the checkpoint, and the next flush recovers without an operator;
- a twenty-save burst costs the index one change;
- the flush reaches the index without waiting out the debounce window;
- a file rewritten in the same batch that removed its directory survives the expansion of that removal.

Measured against this repository through a real daemon, a real container, and the live MCP endpoint:

| | |
|---|---|
| Daemon start | reconciled to generation 13, 172 files, gap closed, record complete |
| Write then search | found **0.2 s** after the write, against a 3 s debounce window |
| Delete then search | gone, one generation later |
| Directory of two files, then removed | both files left the index |
| File written while the daemon was down | picked up by the reconciliation on the next start and found by search |

Bulk escalation is covered by automated tests rather than measured live: this repository has 173 indexed files, so a batch reaching the 500-file floor would mean rewriting it several times over.

### Slice 2.3: Resource policies and Admin UI baseline

**Status:** resource policies complete; the Admin UI baseline is the remaining half.

Implemented:

- `quiet`, `normal`, and `fast` background modes that change what a project actually costs — container CPU limit and concurrent index work — set machine-wide with a per-project override in the registry rather than in the repository manifest, because how much of a machine a project may spend is the machine owner's decision;
- memory left uncapped in every mode unless explicitly configured, because a CPU limit throttles an indexer while a memory limit kills it and leaves the index no better off;
- the parallelism cap passed into the container rather than derived inside it, since the container cannot see the host's policy and would otherwise size itself to the whole machine;
- disk reporting: what the index occupies, how much source it describes, and how much room is left, with `start` and `restart` refusing when less than a gigabyte would remain unless `--yes` is given;
- `lctk project resources` and the resource half of `lctk settings show`.

Verified live against this repository, by inspecting the running container rather than by reading the setting back: `quiet` produced a 1-CPU limit and `LCTK_INDEX_PARALLELISM=1`, `fast` produced no CPU limit and no cap, and clearing the override returned it to the machine's 2 CPUs. Measured disk: 9.98 MiB of index for 1.19 MiB of source across 179 files and two retained generations.

The disk estimate was wrong before it was measured. A guessed ratio of 2× would have predicted 2.4 MiB against an actual 10 MiB, because a shard carries fixed metadata that dominates on a small project and two generations are retained. The model now has that shape, and the roadmap records that it is anchored to one repository and is a guess about large ones.

Remaining:

- add, start, stop, status, and index progress;
- logs/doctor;
- client grants.

These are the Admin UI baseline: a minimal local web surface over an Admin API separate from the project `code` endpoint, whose session bootstrap [security](security.md) still records as an open question.

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
