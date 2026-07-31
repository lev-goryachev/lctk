# Codebase Memory MCP comparative assessment

## Status

Research assessment and architectural reference. The maintainer reviewed the findings and accepted the disposition on 2026-07-26: LCTK may learn from Codebase Memory MCP, but it will not adopt, embed, vendor, wrap, execute, or expose Codebase Memory MCP as a production dependency. [ADR-0010](../adr/0010-codebase-memory-mcp-reference-only.md) records the decision.

Assessment date: 2026-07-26.

Upstream project: [`DeusData/codebase-memory-mcp`](https://github.com/DeusData/codebase-memory-mcp).

This document is a source-based comparative assessment, not a completed LCTK compatibility spike. No Codebase Memory MCP release was installed or executed as part of this assessment, and its performance, quality, security, packaging, and compatibility claims were not independently reproduced. Statements about upstream behavior cite upstream documentation or source where practical and are qualified accordingly.

## Executive conclusion

Codebase Memory MCP, abbreviated **CBM** in this document, is a substantial local code-intelligence product. It demonstrates that a persistent structural graph, broad Tree-sitter parsing, local semantic retrieval, incremental refresh, cross-repository analysis, and a useful MCP tool surface can be delivered as a single native binary with low operational friction.

CBM also overlaps with several long-term LCTK capabilities. It is therefore valuable prior art. Its strongest lessons concern:

- graph-oriented answers that replace repeated file-by-file exploration;
- explicit index-coverage checks before negative or exhaustive claims;
- fast layered parsing and relationship extraction;
- SQLite as a practical local graph and full-text store;
- supervised indexing workers and bounded resource use;
- separating the code-intelligence engine from the model;
- shipping a small, local, dependency-light user experience;
- testing tool contracts, parsers, persistence, and platform behavior aggressively.

The overlap does **not** make CBM a suitable LCTK core or production backend. The two projects optimize for different primary boundaries:

- CBM is primarily a native, account-scoped code-intelligence engine and MCP tool server.
- LCTK is a project-scoped local development platform whose defining contracts include server-assigned project scope, client grants, a stable Streamable HTTP API, isolated project state, actual language-server integration, a constrained runner, and uniform freshness and provenance.

The accepted disposition is therefore:

1. use CBM only as public architectural and product prior art;
2. do not add CBM source, binaries, packages, database formats, tool schemas, daemon protocol, installers, hooks, or generated artifacts to LCTK;
3. do not create a CBM adapter or compatibility layer;
4. keep the LCTK control plane, public MCP boundary, project registry, grant enforcement, lifecycle, watcher journal, orchestration, policy, and normalization logic LCTK-owned;
5. evaluate specialized implementation dependencies independently under existing LCTK backend and language-tool contracts, without allowing any external product to become the LCTK core or public architecture;
6. independently verify any idea inspired by CBM before treating it as an LCTK requirement or implementation result.

This decision avoids two opposite mistakes:

- ignoring strong existing prior art and rebuilding without learning from it;
- allowing an impressive external product to determine LCTK's security model, public API, lifecycle, persistence format, or contributor model.

## Scope of the assessment

The assessment asks five questions:

1. What does CBM appear to provide today?
2. Which CBM capabilities overlap with the LCTK target?
3. Where do the architecture, security model, lifecycle, and contracts differ?
4. Which ideas are worth studying independently?
5. Does CBM invalidate the rationale for LCTK?

The assessment focuses on:

- installation and distribution;
- MCP transport and tool design;
- project selection and authorization;
- process and storage isolation;
- indexing and refresh behavior;
- exact, structural, graph, and semantic search;
- language intelligence;
- persistence, coverage, freshness, and provenance;
- resource management;
- project memory;
- command execution;
- contributor accessibility;
- testing, security, release maturity, and licensing;
- consequences for the LCTK roadmap.

It does not attempt a line-by-line security audit of CBM, reproduce its benchmark paper, or determine the correctness of every supported language grammar.

## Evidence and confidence

### Evidence used

The assessment used the upstream repository and documentation visible on 2026-07-26, including:

- the root [`README.md`](https://github.com/DeusData/codebase-memory-mcp/blob/main/README.md);
- [`CONTRIBUTING.md`](https://github.com/DeusData/codebase-memory-mcp/blob/main/CONTRIBUTING.md);
- [`SECURITY.md`](https://github.com/DeusData/codebase-memory-mcp/blob/main/SECURITY.md);
- [`docs/BENCHMARK.md`](https://github.com/DeusData/codebase-memory-mcp/blob/main/docs/BENCHMARK.md);
- [`docs/CONFIGURATION.md`](https://github.com/DeusData/codebase-memory-mcp/blob/main/docs/CONFIGURATION.md);
- the MCP, daemon, store, pipeline, watcher, semantic, UI, and test source trees;
- the public releases and repository metadata visible on GitHub.

### Confidence labels

Findings in this document use the following implicit confidence model:

- **Source-supported:** behavior is visible in source, tests, or a precise API declaration.
- **Upstream-documented:** behavior is stated by upstream but was not independently run.
- **Inference:** behavior follows from the observed architecture but requires runtime confirmation.
- **Marketing claim:** a comparative or performance statement comes from upstream promotional material and requires independent reproduction.

### Important limitations

- No upstream binary or installer was executed.
- No index was built locally with CBM.
- No Windows, macOS, or Linux runtime matrix was exercised.
- No benchmark result was reproduced.
- No graph-quality precision or recall study was performed.
- No supply-chain or file-level license audit was performed.
- Upstream is pre-1.0 and changes rapidly; exact counts, names, and behavior may change.

## Product intent comparison

### CBM's apparent product intent

CBM describes itself as a structural-analysis backend for coding agents. It builds a persistent knowledge graph from a repository and exposes search, traversal, architecture, change-impact, code-snippet, and related operations through MCP and CLI tools. The model remains in the MCP client; CBM does not embed a conversational model.

Its user-experience thesis is deliberately simple:

```text
download one native binary
→ configure detected coding clients
→ index a repository
→ query a persistent graph locally
```

The upstream project emphasizes broad language coverage, fast indexing, local processing, no mandatory API key, no Docker requirement, and low-token structural answers.

### LCTK's product intent

LCTK is not defined only by graph search. Its target is durable local coding infrastructure with replaceable clients and internal engines. Its accepted boundaries include:

- one server-known registered root per stable `project_id`;
- one project-scoped MCP endpoint;
- server-enforced route and grant agreement;
- separate project indexes, memory, runtime metadata, and volumes;
- a stable LCTK-owned public tool API;
- persistent exact search;
- actual language-server operations;
- AST and structural analysis;
- local semantic search;
- persistent graph intelligence;
- Git awareness;
- a constrained project runner;
- freshness and provenance;
- host-managed lifecycle and filesystem observation.

The distinction is material. CBM is evidence that a strong code-intelligence engine is useful and can be operationally simple. It is not evidence that LCTK's project authority, runner, grant, lifecycle, or freshness requirements are unnecessary.

## High-level comparison

| Area | CBM, based on reviewed evidence | LCTK target | Assessment |
|---|---|---|---|
| Primary role | Native code-intelligence product | Project-scoped local coding platform | Different center of gravity |
| Distribution | Single static C binary, multiple package channels | One Go executable plus managed project runtime | CBM is simpler for users today |
| Public MCP transport | Primarily JSON-RPC MCP over stdio | Streamable HTTP | Contract mismatch |
| Long-lived coordination | Per-account native daemon | LCTK host daemon and embedded gateway | Similar pattern, different authority |
| Project selection | Tool/session context and caller-supplied project names | Route-bound server context | LCTK is stricter |
| Client authorization | Same-user process/IPC controls and optional root restriction | Per-client project grants and capability profiles | LCTK is stricter and more granular |
| State separation | Per-project databases in an account-wide cache | Per-project mounts, indexes, memory, networks, and volumes | LCTK targets a stronger boundary |
| Exact content search | Live grep-like search over indexed files | Persistent indexed literal/regex search | Not equivalent |
| Structural search | Strong graph and name/path search | Planned AST and graph tools | Major overlap |
| Language breadth | Very broad Tree-sitter coverage | Incremental language adapters | CBM is far ahead in breadth |
| Semantic language intelligence | Internal heuristic "Hybrid LSP" passes | Actual language servers | Different accuracy and lifecycle model |
| Semantic retrieval | Local static-vector and multi-signal ranking | Local replaceable embedding/vector layer | Similar goal, different implementation |
| Change detection | Git polling and incremental reindex behavior | Native host watcher, journal, reconciliation | LCTK targets lower latency and non-Git support |
| Freshness | Generations, hashes, coverage, timestamps in parts of the API | Uniform response-level freshness/provenance | CBM provides useful ideas, not the full contract |
| Runner | No general agent-facing build/test/lint runner | Separate constrained runner | Missing from CBM |
| Project memory | ADR-oriented persisted content | Typed project-memory CRUD and decisions | CBM is narrower |
| Cross-repository graph | Supported deliberately | Project endpoints isolate repositories | Different policy |
| UI | Optional graph visualization | Minimal lifecycle and diagnostics Admin UI | Different purpose |
| Contributor language | Large C/C++ systems codebase | Go for LCTK-owned code | LCTK should have a lower core contribution barrier |
| Stability | Active pre-1.0 product with visible drift | Stable LCTK-owned contract is an explicit goal | Do not expose CBM contracts as LCTK contracts |
| License | MIT plus vendored/generated dependency obligations | Apache-2.0 | Generally compatible, but no adoption is planned |

## Detailed findings

## 1. Packaging and operational model

### CBM

CBM advertises static binaries for Windows amd64, macOS amd64/arm64, and Linux amd64/arm64. It also advertises multiple installation channels, including npm, PyPI, Homebrew, Scoop, Winget, Chocolatey, AUR, and `go install` wrappers or installers.

The most important architectural property is not the number of package channels. It is that the user-facing product is one native executable with embedded or vendored functionality. SQLite, parsing grammars, semantic vector data, MCP handling, CLI functions, daemon coordination, and the optional UI are delivered without requiring a separate database or container runtime.

This substantially reduces:

- installation prerequisites;
- startup coordination;
- image download and disk cost;
- Docker Desktop path and mount failures;
- container networking failures;
- support burden for users who only need read-only code intelligence.

### LCTK

LCTK currently targets Docker Desktop for project services while keeping the host daemon and embedded gateway native. This buys explicit project mounts, reusable images, dependency containment, persistent volumes, and a stronger execution boundary. It also creates a materially heavier product.

### Lesson

LCTK must treat operational simplicity as a first-class quality, not as a late packaging concern. Every project service, image, and process must justify its existence. The accepted container model remains in force, but CBM is evidence that users will compare LCTK against a one-binary experience, not only against other container platforms.

No conclusion should be drawn that LCTK ought to import CBM or change the accepted container boundary without a new ADR and measured evidence.

## 2. MCP transport and public boundary

### CBM

The reviewed CBM MCP surface is primarily a stdio JSON-RPC server. The executable is configured directly in client MCP settings. Newer architecture also uses a shared per-account daemon behind thin frontends. Native IPC coordinates sessions, indexing jobs, watchers, and the optional UI.

The optional UI has an HTTP service and internal RPC path, but that does not establish a project-routed Streamable HTTP MCP contract equivalent to LCTK's accepted endpoint.

### LCTK

LCTK accepts this public route:

```text
http://127.0.0.1:4444/projects/{project_id}/mcp
```

The route, credential, registry, and grant determine the project before a tool is dispatched. The external client does not select an authoritative project through a tool argument.

### Trade-off

Stdio is easy to configure per client and naturally inherits the launching user's identity. Streamable HTTP provides a stable long-lived endpoint, virtual project servers, centralized lifecycle, explicit grants, and support for clients that do not launch a separate process.

LCTK's transport is more complex because it is solving a different authority and lifecycle problem. CBM's stdio design should not replace or leak into the LCTK public contract.

## 3. Project identity and authorization

### CBM

CBM demonstrates careful same-user native IPC controls. Reviewed evidence describes owner-private Unix sockets or Windows named pipes, peer process validation, build-cohort checks, and fail-closed behavior for unsafe endpoint conditions. This is meaningful local security engineering.

However, the apparent project model remains account-oriented:

- projects are persisted under a shared cache root;
- tools can list named projects;
- query tools accept a `project` argument;
- mutation tools can index or delete named projects;
- `index_repository` accepts a repository path;
- `CBM_ALLOWED_ROOT` can restrict indexing to a root, but is unset by default;
- API declarations allow an absent root restriction in some contexts.

The upstream troubleshooting guidance acknowledges that a caller may need to specify the correct project to avoid wrong-project results. That is incompatible with LCTK's rule that project scope cannot be corrected or changed by a model argument.

### LCTK

LCTK requires:

1. a host-canonical registered path;
2. a stable project ID;
3. a route containing that project ID;
4. a client grant authorizing that project and capability profile;
5. server-side context injection;
6. rejection of conflicting or cross-project attempts before tool dispatch.

### Conclusion

CBM's account security is worth studying, particularly native IPC ownership and admission barriers. Its project-selection model is not suitable as LCTK's authorization model. LCTK must continue to own project identity and grant enforcement.

## 4. Isolation and process model

### CBM

CBM appears to use:

- one account coordination daemon;
- thin MCP frontends;
- supervised indexing workers;
- per-project database mutation locks;
- per-project SQLite databases;
- shared account-level configuration and cache roots.

Supervised workers improve reliability and allow indexing memory to be reclaimed when a worker exits. Per-project locks serialize conflicting mutations while allowing unrelated projects to proceed.

This is good process supervision. It is not a filesystem or network sandbox. Native processes retain the permissions of the current OS account unless separately restricted by the operating system.

### LCTK

LCTK's project runtime targets separate project stacks and mounts. Code-intelligence services receive only their project's read-only source mount. The runner receives a separate writable mount and command policy. Neither receives the Docker socket or another project's volume.

### Conclusion

The CBM process-supervision ideas are relevant. The CBM isolation model does not supersede the LCTK project runtime. In particular, indexing-worker isolation and runner isolation must not be conflated.

## 5. File discovery and exclusions

### CBM

CBM documents layered exclusions including hard-coded dependency or VCS paths, `.gitignore`, and `.cbmignore`; symlinks are skipped. It also supports custom file-extension mappings.

### LCTK

LCTK requires project-relative normalized paths, default exclusions, manifest additions, safe path resolution, watcher exclusions, and an explicit policy for generated code.

### Lessons

Useful ideas to validate independently include:

- layered exclusion precedence;
- a project-specific ignore file using familiar Git ignore syntax;
- explicit reporting of excluded or unparsed files;
- treating symlink policy as both a correctness and security decision;
- allowing extension mapping without allowing a repository manifest to change the authoritative root.

LCTK should not copy CBM's configuration names or semantics automatically. The LCTK manifest remains a versioned LCTK-owned contract.

## 6. Indexing pipeline

### CBM

CBM describes a multi-pass pipeline that discovers files, parses Tree-sitter ASTs, extracts definitions and relationships, resolves imports and calls, links routes and services, computes graph enrichments, and persists a SQLite graph. The source layout separates pipeline passes, storage, parsing, semantic data, discovery, and watchers.

The pipeline is optimized for throughput. Upstream describes RAM-first processing, compressed reads, an in-memory SQLite build, and a final dump. Indexing work is supervised and memory-budgeted.

### LCTK

LCTK's indexing architecture is layered by capability:

1. file inventory;
2. exact search;
3. AST and outlines;
4. symbols and language-server availability;
5. semantic chunks;
6. graph and repository map enrichment.

Each capability can become available before later layers finish. Backends may have different generations as long as the response states that explicitly.

### Trade-off

A tightly integrated pipeline can be extremely fast and can optimize across passes. A modular multi-backend platform can use best-of-breed engines and isolate failures, but must solve consistency, orchestration, duplicate work, schema translation, and resource contention.

CBM is evidence that excessive backend fragmentation can be costly. LCTK should keep the number of production components small. It does not follow that LCTK should adopt CBM's integrated pipeline or storage schema.

## 7. Tree-sitter and language breadth

### CBM

Upstream claims approximately 158 vendored Tree-sitter grammars compiled into the binary and publishes language benchmark material. The benchmark distinguishes stronger and weaker languages and records limitations such as incomplete properties or call resolution for difficult language constructs.

The breadth is a major product advantage. Even shallow structural support can answer file, symbol, and architecture questions in polyglot repositories.

### LCTK

LCTK plans to add language adapters in verified slices, initially around TypeScript/JavaScript, Python, Rust, and C/C++. It prioritizes explicit lifecycle, diagnostics, resource behavior, and actual language-tool evidence over a broad unsupported claim.

### Trade-off

- CBM favors broad built-in syntax coverage with language-specific refinement.
- LCTK favors narrower, separately verified adapters and real language-server behavior.

Neither approach dominates every use case. Breadth is valuable for repository mapping; depth is essential for compiler-aware definitions, references, and diagnostics.

### Lesson

LCTK should separate claims for:

- file recognition;
- parse success;
- outline extraction;
- definition extraction;
- call-edge resolution;
- type-aware references;
- diagnostics.

A language must not be described simply as "supported" without stating which capability level was verified.

## 8. Structural graph

### CBM

The reviewed graph model includes nodes such as projects, packages, folders, files, modules, classes, functions, methods, interfaces, enums, types, routes, and infrastructure resources. Relationships include containment, definitions, calls, imports, implementations, inheritance, usages, HTTP or asynchronous calls, tests, type usage, and change correlations.

The product exposes graph search, path traversal, schema inspection, architecture summaries, impact analysis, and a read-only Cypher-like query subset. It also computes clustering and supports cross-repository links.

### LCTK

LCTK plans stable task-oriented tools such as:

- `symbol_find`;
- `callers_find`;
- `callees_find`;
- `impact_analyze`;
- `repository_map`.

The stable public API is intended to hide the internal graph engine and schema.

### Trade-off

Raw graph-query access is powerful for advanced agents and debugging. It also exposes storage concepts, encourages client dependence on unstable labels and relationships, and makes backend replacement difficult.

### Conclusion

CBM strongly validates graph-oriented code intelligence as a product capability. It does not change ADR-0004: LCTK must expose stable user-action tools rather than make an internal graph schema its primary public API. A low-level diagnostic query surface, if ever added, requires a separate policy and must not define compatibility.

## 9. Exact and regex content search

### CBM

CBM provides several search modes:

- persistent graph and symbol search in SQLite;
- FTS5/BM25-style text indexing for graph content;
- structural name and path patterns;
- a `search_code` tool described as graph-augmented grep over indexed files.

Reviewed source and security allowlists indicate that raw code search invokes grep-like live source scanning rather than relying on a complete persistent raw-content index.

### LCTK

LCTK's `exact_search` contract requires:

- a persistent content index;
- literal and regex modes;
- live saved working-tree content, including dirty and untracked files;
- project-relative path and language filtering;
- bounded incremental updates;
- restart reuse;
- offline reconciliation;
- pagination, freshness, provenance, and typed failures.

### Conclusion

CBM's content search does not remove the need for LCTK's exact-search evaluation. Live scanning can be an excellent fallback or correctness oracle, but it is a different persistence and latency contract. LCTK must not claim equivalence between graph FTS, symbol search, and persistent exact source search.

## 10. Language intelligence and the "Hybrid LSP" term

### CBM

CBM uses the name "Hybrid LSP" for internal language-specific static-resolution passes layered over Tree-sitter. Reviewed descriptions include import maps, cross-file registries, receiver inference, generic substitution, standard-library knowledge, inheritance handling, and language-specific call resolution.

This is technically interesting and can improve call edges substantially without running external language servers. It is not the Language Server Protocol and does not establish interoperability with standard language-server processes.

Likely strengths include:

- low startup overhead;
- no per-project toolchain installation;
- predictable embedded operation;
- broad fallback behavior;
- graph-specific optimization.

Likely limitations include:

- heuristic resolution;
- incomplete compiler and build-system context;
- difficulty matching rapidly evolving language semantics;
- duplicated implementation effort across languages;
- no authoritative compiler diagnostics;
- no guarantee of parity with `gopls`, Pyright, rust-analyzer, clangd, or TypeScript language tooling.

### LCTK

LCTK explicitly plans actual language adapters for symbols, definitions, references, implementations, and diagnostics. Tree-sitter may supply outlines and chunks independently, but it does not replace the language server.

### Conclusion

LCTK should learn from CBM's layered degradation model: syntax and structural answers can remain available while deeper language services are unavailable. LCTK must not use the term LSP for a non-LSP heuristic implementation, and it must preserve provenance so clients know whether an answer came from Tree-sitter, a graph heuristic, or an actual language server.

## 11. Semantic search

### CBM

Upstream describes a local semantic mechanism derived from `nomic-embed-code`. Reviewed source-oriented findings indicate that the shipped runtime uses a static token-vector table and combines lexical, structural, graph, and similarity signals. It does not appear to run the original full embedding model for every query and chunk.

This design can provide:

- no external API dependency;
- low operational overhead;
- deterministic local behavior;
- modest packaged model data;
- useful vocabulary-bridging signals;
- integration with graph proximity and clone detection.

It may not provide the contextual representation quality of a current full code embedding model. Upstream quality and token-reduction claims require independent reproduction.

### LCTK

LCTK plans AST-aware chunks, a local CPU embedding model, a replaceable vector adapter, incremental invalidation, and hybrid lexical/vector ranking.

### Lessons

LCTK should benchmark multiple semantic strategies rather than assume a full model is always necessary. A cheap static-vector or sparse semantic signal may be useful as:

- a first-stage candidate generator;
- a fallback on resource-constrained machines;
- one signal in hybrid ranking;
- a repository-map enrichment.

This is an architectural lesson only. LCTK will not incorporate CBM's vector table, generated data, semantic implementation, or ranking schema.

## 12. Clone detection and semantic graph edges

### CBM

Upstream describes `SIMILAR_TO` relationships based on MinHash/LSH and `SEMANTICALLY_RELATED` relationships based on semantic similarity. These can expose duplicated implementations and conceptually related code through the same graph.

### LCTK relevance

These are useful optional graph enrichments but are not part of the first required vertical slice. They can be expensive, noisy, or misleading if exposed without confidence and provenance.

### Lesson

If LCTK later adds similarity relationships, every result should include:

- the method used;
- a score with defined meaning;
- the indexed generation;
- language and path scope;
- whether the relationship is deterministic or model-derived;
- a warning that similarity is not semantic equivalence.

## 13. Incremental refresh and watcher model

### CBM

The reviewed watcher is Git-based polling. It tracks HEAD and dirty working-tree signatures, uses adaptive polling intervals, and triggers reindexing. Source comments state that non-Git projects are not normally watched for content changes.

Benefits include:

- portability without native watcher backends;
- natural detection of branch and commit movement;
- coalescing many filesystem events into repository state;
- avoidance of bind-mount watcher inconsistencies.

Costs include:

- polling latency;
- repeated Git status work;
- weaker non-Git behavior;
- less direct event provenance;
- possible delay between a save and graph freshness.

### LCTK

LCTK runs a native host watcher, writes normalized events to a durable change journal, batches updates, reconciles after downtime or overflow, and publishes freshness state. The watcher is an accelerator rather than the sole authority.

### Lesson

LCTK should consider Git state a separate reconciliation and classification signal, not a replacement for the host watcher. In particular:

- native events provide low-latency saved-file awareness;
- Git identifies branch, checkout, merge, and dirty-state transitions;
- manifests and hashes recover from missed events;
- bulk-change classification avoids event storms.

This layered design is more complex but better matches the LCTK freshness contract.

## 14. Persistence and database model

### CBM

CBM uses SQLite for persistent graph state and account configuration. Upstream documents WAL mode and ACID persistence. It also supports a compressed team-shared graph artifact that can seed a local database before incremental catch-up.

SQLite is attractive because it provides:

- a single portable file;
- transactions;
- mature recovery behavior;
- FTS5;
- broad tooling;
- straightforward backup and inspection;
- no separate server.

### LCTK

LCTK has not selected every persistent implementation. It requires project separation, migrations, schema/version metadata, crash-safe generation publication, and explicit corruption/rebuild states.

### Lessons

SQLite remains a strong candidate for LCTK-owned registry, journal, metadata, and possibly graph implementations. Any adoption must follow a separate evaluation and schema decision. LCTK will not consume CBM databases or compressed graph artifacts because doing so would couple LCTK to CBM's schema, migrations, project identity, and release cadence.

## 15. Coverage, freshness, and provenance

### CBM

CBM contains several valuable coverage concepts:

- project index timestamps;
- per-file hashes and metadata;
- store generations;
- parse-partial and read/extraction status;
- records for oversized, skipped, ignored, or deliberately unindexed files;
- targeted `check_index_coverage` guidance before exhaustive or negative claims;
- cursor invalidation when a generation changes.

Upstream documentation correctly warns that a clean coverage result means no recorded gap, not proof of semantic completeness.

### LCTK

LCTK requires a uniform compact envelope with commit, dirty state, index generation, pending file count or lag, freshness state, backend provenance, and last successful update. Different index layers may report different generations.

### Conclusion

Coverage reporting is one of the strongest ideas to learn from. LCTK should preserve two distinct concepts:

1. **freshness:** whether the indexed representation reflects current saved source;
2. **coverage:** which relevant files or ranges were successfully represented and at what capability level.

Neither implies the other. A fresh graph can have parse gaps, and a complete old graph can be stale.

LCTK should add capability-specific coverage to future graph and AST contracts, but the schema must be LCTK-owned and validated independently.

## 16. Repository architecture summaries

### CBM

CBM exposes a compact architecture view containing languages, packages, entry points, routes, hotspots, boundaries, layers, and clusters. This is aligned with the practical need to answer broad repository questions without many tool calls.

### LCTK

LCTK plans a repository map and task-oriented graph operations.

### Lesson

A repository map should be treated as an indexed product with:

- generation and source state;
- explicit size and token budget;
- stable project-relative paths;
- confidence for inferred boundaries;
- pagination or drill-down references;
- a distinction between declared structure and inferred clusters.

The CBM architecture summary validates prioritizing this capability earlier than a general low-level graph query language, but it does not authorize a roadmap change by itself.

## 17. Git awareness and impact analysis

### CBM

CBM maps Git changes to graph symbols and estimates blast radius or risk. This is a high-value agent workflow because it combines source changes with precomputed relationships.

### LCTK

LCTK already plans Git status, diffs, changed files, callers/callees, and impact analysis in separate stages.

### Lesson

LCTK should design Git and graph contracts together. Impact output should distinguish:

- directly changed symbols;
- statically resolved dependents;
- heuristic or semantic neighbors;
- test relationships;
- runtime-observed relationships, if any;
- confidence and maximum traversal depth;
- stale or incomplete graph regions.

Risk classification should remain advisory and explain its evidence.

## 18. Cross-service and cross-repository intelligence

### CBM

CBM intentionally links HTTP routes, call sites, asynchronous channels, and repositories indexed into the same account-level system. This supports fleet and multi-service architecture exploration.

### LCTK

LCTK's accepted first boundary is deliberately project-specific. A project may contain a monorepo, but one project endpoint does not access another registered project. Cross-project access requires an explicit future product and authorization design.

### Trade-off

Cross-repository graphs are valuable but conflict with simple isolation. A global graph can reveal names, paths, services, or relationships across repositories even when a client should only see one project.

### Conclusion

LCTK must not copy account-wide cross-repository behavior into project endpoints. If cross-project intelligence is added later, it requires:

- an explicit aggregate scope object;
- grants covering every included project;
- provenance on every node and edge;
- revocation and reindex behavior;
- tests proving that removed projects no longer leak data;
- a separate public contract and ADR.

## 19. Project memory and ADR handling

### CBM

CBM persists an ADR-oriented content record per project and preserves it across reindexing. This is useful durable context, but it is narrower than a general memory subsystem.

### LCTK

LCTK plans explicit project-memory CRUD, decision records, provenance, confidence, and review metadata.

### Lesson

Architectural decisions are a good first typed memory category because they are deliberate, reviewable, and durable. LCTK should avoid starting with opaque unlimited notes. A future memory design should distinguish:

- accepted decisions;
- working assumptions;
- open questions;
- user-authored facts;
- agent-derived observations;
- generated summaries;
- source and review timestamps.

The repository's tracked `docs/adr/` remains authoritative for project architecture documentation. A local memory record must not silently override tracked documentation.

## 20. Runner and command execution

### CBM

CBM starts subprocesses for internal implementation tasks such as Git operations, grep-like search, update behavior, and supervised indexing. It does not expose a general constrained project runner equivalent to the LCTK target.

### LCTK

LCTK plans a separate runner with:

- one project mount;
- fixed working directory;
- typed command policy;
- timeout and cancellation;
- output limits;
- process-tree cleanup;
- CPU, RAM, and PID controls;
- explicit network mode;
- no Docker socket;
- local audit records.

### Conclusion

CBM does not invalidate the runner requirement. Internal process supervision is useful implementation prior art, but indexing workers and agent-invoked project commands have different trust and policy requirements.

## 21. Admin UI

### CBM

CBM's optional UI emphasizes graph visualization. It is useful for exploring graph nodes, edges, clusters, and cross-repository structure.

### LCTK

LCTK's Admin UI is primarily operational:

- add or remove a project;
- start, stop, or restart;
- inspect indexing progress and freshness;
- inspect logs and diagnostics;
- configure runner network and resources;
- manage client grants.

### Lesson

Graph visualization is attractive but is not a substitute for lifecycle and policy administration. LCTK should keep the first Admin UI small. A graph explorer can be considered later as a separate capability and must obey the same project grant and browser-origin rules.

## 22. Resource management

### CBM

Reviewed CBM design includes memory-budget detection, explicit overrides, worker-budget subdivision, extraction backpressure, a limit on concurrent physical indexing jobs, job coalescing, supervised worker exit, and idle store eviction.

These are mature and directly relevant operational ideas.

### LCTK

LCTK plans quiet, normal, and fast modes; per-project resource estimates; interactive priority; background indexing; optional on-demand processes; and a 16 GB CPU-only baseline.

### Lessons

LCTK should define resource control at three levels:

1. **machine:** total indexing concurrency, shared inference, disk budget;
2. **project:** CPU, memory, process, and index size limits;
3. **operation:** timeout, cancellation, priority, and output limits.

Worker termination as a reliable way to reclaim indexing memory is worth considering for LCTK-owned workers even in Go, especially for native libraries or large transient indexes. It must be evaluated rather than copied.

## 23. Testing and security engineering

### CBM

Upstream documents a large C test suite, sanitizers, static analysis, formatting, security allowlists, network-egress tests, MCP robustness checks, vendored dependency integrity checks, release signatures, checksums, provenance, and antivirus scanning.

Source-based review also found dedicated tests around pipeline behavior, graph stores, HTTP linking, MCP handling, watcher behavior, IPC, indexing supervision, and resilience.

These are positive maturity signals. They do not replace independent review, and the reported counts and gates were not run during this assessment.

### LCTK lessons

LCTK should preserve and extend its evidence-oriented delivery policy:

- black-box MCP contract tests;
- cross-project negative tests;
- restart and persistence tests;
- platform-specific path tests;
- fuzzing for schemas, cursors, path handling, and protocol framing;
- race detection for Go code;
- dependency and release provenance;
- tests proving that source data and secrets are absent from diagnostics unless explicitly requested.

CBM's security work is evidence that a local single-user tool still needs serious IPC, update, parser, and release hardening.

## 24. API stability

### CBM

The repository is active and pre-1.0. Reviewed materials showed visible naming and metadata drift, including differing counts for languages or tools and aliases such as `trace_path` and `trace_call_path`. Different package and documentation files may lag the newest release.

Rapid evolution is normal for a pre-1.0 product. It is incompatible with making CBM's current schemas the LCTK public compatibility contract.

### LCTK

ADR-0004 requires a stable aggregated API whose tool names and response contracts do not depend on internal engine names.

### Conclusion

LCTK should learn from CBM's task coverage while independently designing a smaller stable surface. Tool proliferation and exposure of raw engine concepts should be resisted. Every public tool must have:

- a user-action meaning;
- bounded output;
- pagination;
- cancellation and timeout behavior;
- typed failures;
- project-relative paths;
- provenance and freshness where relevant;
- versioning and compatibility tests.

## 25. Contributor accessibility

### CBM's C trade-off

CBM's implementation is predominantly C, with some C++ and other support code. C remains an important and widely used language, especially for operating systems, embedded software, databases, compilers, and performance-sensitive libraries. General language popularity does not directly measure the available contributor pool for a modern cross-platform MCP and developer-tooling product.

A contributor to a large C system may need to understand:

- manual ownership and lifetime rules;
- allocators and arenas;
- pointer validity and error cleanup;
- thread synchronization;
- native IPC;
- process supervision;
- Windows and POSIX differences;
- SQLite integration;
- vendored parser grammars;
- sanitizer and static-analysis failures;
- ABI and static-linking constraints.

This creates a higher barrier for ordinary feature contributions and reviews than an equivalent Go control-plane change.

### C benefits

The choice also enables important user-facing properties:

- one compact native binary;
- low startup overhead;
- no managed runtime installation;
- close integration with SQLite and Tree-sitter;
- explicit memory control;
- predictable embedding of vendored assets.

CBM therefore makes a rational trade:

```text
higher implementation and contribution complexity
→ lower installation and runtime complexity
```

### LCTK's Go trade-off

ADR-0006 selects Go for LCTK-owned daemon, CLI, control-plane, adapter, and supporting-library code. Go offers:

- memory safety for ordinary code;
- a strong cross-platform standard library;
- straightforward HTTP, JSON, concurrency, and process APIs;
- fast builds and simple tests;
- a race detector;
- simpler review and refactoring;
- a contributor pool aligned with cloud, infrastructure, MCP, and developer tooling.

Go does not make LCTK automatically simple. Docker, MCP, watchers, LSPs, search engines, persistence, authentication, Windows/macOS path rules, and release engineering can still create a high architectural barrier.

A useful model is:

```text
contributor difficulty
= language difficulty
+ architecture difficulty
+ toolchain difficulty
+ verification difficulty
```

LCTK should not spend the accessibility gained from Go by creating unnecessary services, protocols, or abstraction layers.

### Accepted contributor conclusion

LCTK-owned core and orchestration code remain in Go. CBM will not introduce a large C subsystem, fork, vendored engine, or dual-language core into the repository. Architectural ideas may be reimplemented independently in idiomatic Go when they fit an accepted LCTK requirement and have their own tests.

## 26. Licensing and supply-chain posture

### CBM

CBM is MIT-licensed at the repository level and includes many vendored grammars, libraries, and generated semantic data with their own obligations. MIT code is generally compatible with an Apache-2.0 project when notices are retained, and Apache-2.0-derived data may also be compatible subject to attribution and NOTICE requirements.

A real adoption would require a file-level audit covering:

- CBM's MIT notice;
- every vendored Tree-sitter grammar;
- SQLite and other vendored libraries;
- generated vector data and its source model;
- optional UI dependencies;
- package installers and release assets.

### LCTK disposition

No adoption is planned, so LCTK will not add CBM to `THIRD_PARTY_NOTICES.md` merely for reading its public design. If a future implementation copies code or generated data rather than independently implementing an idea, that would contradict ADR-0010 and require an explicit superseding decision and license review.

## 27. Upstream maturity and marketing claims

### Positive maturity indicators

At assessment time, GitHub showed an active project with frequent commits, many releases, a large public audience, contributors, CI, security documentation, tests, and multiple distribution channels. These indicate substantial execution and user interest.

### Claims requiring reproduction

Upstream makes strong claims including:

- average repositories indexed in milliseconds;
- the Linux kernel indexed in minutes;
- sub-millisecond graph queries;
- large token reductions;
- improved answer quality and fewer tool calls;
- broad language counts and quality tiers.

The repository provides benchmark documentation and cites a preprint. LCTK has not reproduced these numbers. They must remain attributed upstream claims, not facts in LCTK product planning.

### Documentation drift

Differences in tool counts, language counts, aliases, and version references were visible across upstream files. This is not unusual for rapid pre-1.0 development, but it reinforces the decision not to build an LCTK compatibility promise around CBM's current surface.

## Capability-by-capability disposition

| CBM idea or capability | LCTK disposition | Reason |
|---|---|---|
| Single-binary user experience | Study and use as a simplicity benchmark | Strong user benefit |
| Shared account daemon | Study lifecycle and admission patterns | Similar coordination problem, different authority |
| Native same-user IPC hardening | Study for daemon management API | Useful security prior art |
| CBM daemon or IPC protocol | Do not adopt | Would create an external core boundary |
| CBM MCP tools and schemas | Do not adopt | LCTK owns stable public API |
| Caller-supplied project selection | Reject | Conflicts with route-bound scope |
| Optional allowed-root restriction | Insufficient as LCTK authority | LCTK root is mandatory and registry-bound |
| Per-project SQLite files | Study | Simple persistence pattern |
| CBM database/artifact format | Do not consume | Version and schema coupling |
| Broad Tree-sitter parsing | Study capability tiers and testing | Valuable breadth lesson |
| Vendored CBM grammars | Do not import through CBM | No CBM code or asset dependency |
| Multi-pass graph extraction | Study pass boundaries | Strong data-pipeline pattern |
| Raw Cypher public access | Do not make primary API | Backend leakage and stability risk |
| Task-oriented graph operations | Adopt the product principle independently | Matches LCTK public API direction |
| Live grep code search | Keep only as oracle/fallback concept | Does not satisfy persistent exact-search contract |
| "Hybrid LSP" inference | Study layered fallback | Does not replace actual LSP |
| Actual CBM inference implementation | Do not adopt | External engine and language-core coupling |
| Static semantic token vectors | Consider as a future benchmark candidate, independently | Potential low-resource mode |
| CBM vector data/ranking code | Do not adopt | External generated-data and schema dependency |
| MinHash/LSH clone detection | Study for later graph enrichment | Useful but not first-slice scope |
| Git polling watcher | Use as reconciliation inspiration only | Native events and non-Git support remain required |
| Coverage checks | Adopt the principle independently | Prevents unsupported negative claims |
| Uniform LCTK freshness envelope | Continue as planned | Stronger than partial engine metadata |
| Supervised index workers | Study and evaluate | Useful memory and crash containment |
| Resource budgets and backpressure | Adopt the principle independently | Necessary for 16 GB baseline |
| Graph visualization UI | Defer | Admin UI has a different first purpose |
| ADR memory | Study as first typed memory category | Narrow, deliberate durable context |
| Cross-repository graph | Reject for ordinary project endpoints | Conflicts with isolation |
| General runner | Build as LCTK-owned boundary | CBM does not provide it |
| Release signing and provenance | Continue and strengthen | Good supply-chain practice |
| C implementation in LCTK core | Reject | Contributor and maintenance cost |

## What CBM changes in LCTK's understanding

CBM does change several planning assumptions even though it will not be adopted.

### 1. Graph value is no longer speculative

The existence and adoption of graph-oriented agent tooling support the LCTK goal of callers, callees, impact analysis, and repository maps. LCTK still needs its own evidence for quality and UX.

### 2. Operational simplicity is a competitive requirement

A containerized multi-service architecture must produce benefits that justify its installation and resource cost. LCTK should avoid creating separate services merely because an engine can be separated.

### 3. Broad syntax support and deep semantic support should be distinct

A fast Tree-sitter layer can cover many languages while actual language servers provide depth for selected ecosystems. LCTK should make this layered capability model explicit.

### 4. Coverage is as important as freshness

Agents need to know not only whether an index is current, but also whether relevant files and constructs were successfully represented.

### 5. Repository maps deserve early product attention

A compact architecture summary can reduce exploration cost before every advanced graph query exists.

### 6. Resource management cannot be postponed

Large indexing pipelines require budgets, backpressure, cancellation, worker supervision, and observability from the beginning.

### 7. A large native engine creates contributor concentration

High-performance C can produce an excellent binary while concentrating maintenance in a smaller expert group. LCTK should preserve Go as the language of its owned core and keep interfaces understandable to ordinary infrastructure contributors.

## What CBM does not change

The following accepted LCTK requirements remain justified:

- route-bound project scope;
- client grants;
- LCTK-owned Streamable HTTP gateway;
- project-specific state and lifecycle;
- stable aggregated public tools;
- persistent exact/regex search;
- native host watcher and durable journal;
- actual language-server adapters;
- constrained project runner;
- uniform freshness and provenance;
- no direct Docker administration through coding tools;
- Go for LCTK-owned code;
- small verifiable vertical slices.

## Alternatives considered

## Alternative A: stop LCTK and recommend CBM

This would be reasonable only if LCTK's goal were limited to fast local graph search and code exploration. It would minimize implementation effort and give users immediate capabilities.

It was rejected because it abandons defining LCTK requirements:

- route-bound authority;
- granular client grants;
- stable HTTP endpoint;
- constrained execution;
- actual LSP integration;
- uniform freshness/provenance;
- LCTK-owned lifecycle and policy.

## Alternative B: adopt CBM wholesale as the LCTK core

This would accelerate graph, AST, semantic, and architecture functionality.

It was rejected because CBM's public transport, project model, daemon, database, tools, update lifecycle, and contributor language would become LCTK's architecture. LCTK would retain substantial façade work while losing ownership of its most important contracts.

## Alternative C: use CBM as a wrapped internal backend

A previous analysis considered placing CBM behind an LCTK-owned adapter or project container.

The maintainer explicitly rejected this direction on 2026-07-26. No adapter, wrapper, subprocess integration, pinned binary, vendored source, or compatibility mode will be created. The reason is architectural independence: the LCTK core and its operational wrapping must remain LCTK-owned rather than becoming a shell around an external product.

## Alternative D: fork or vendor CBM

This would provide control over behavior and releases.

It was rejected because it would create a large C maintenance burden, duplicate upstream work, complicate notices and security updates, increase contributor barriers, and make LCTK responsible for a rapidly evolving multi-language engine.

## Alternative E: copy selected CBM code or generated data

This could accelerate individual features.

It was rejected. LCTK may independently implement general architectural ideas, algorithms, or product patterns, but it will not copy CBM source, schemas, generated embeddings, database layouts, grammars as a CBM bundle, or tool definitions. Any third-party library selected independently must pass its own evaluation and notice process.

## Alternative F: use CBM only as prior art

This is the accepted alternative.

It preserves:

- independent LCTK architecture;
- Go contributor accessibility;
- LCTK-owned public contracts;
- freedom to select or replace specialized libraries and engines;
- ability to learn from a working product;
- clear attribution without runtime or source dependency.

## Accepted architecture boundary

The phrase **LCTK-owned core** means that the following are designed, implemented, versioned, and tested by LCTK:

- the `lctk` CLI and host daemon;
- project registration and canonical host-path binding;
- local registry and migrations;
- client credentials and grants;
- the embedded Streamable HTTP gateway;
- public route and tool contracts;
- lifecycle state and typed errors;
- host watcher normalization and change journal;
- index orchestration and freshness aggregation;
- project policy and manifest validation;
- Docker/OCI lifecycle integration;
- runner policy and command boundary;
- Admin API and UI authorization;
- backend adapters and normalized DTOs;
- audit and diagnostics contracts.

This does not mean LCTK must implement every parser, search algorithm, language server, database, container runtime, or model from first principles. The project may use libraries and specialized tools selected through explicit contracts and evaluations. Those dependencies must remain replaceable implementation details and must not become an additional control plane, public API authority, project registry, grant system, or product lifecycle owner.

CBM specifically is reference-only and is not an eligible production dependency under the accepted decision.

## Guidance for future implementation

When a contributor derives an idea from CBM or similar prior art:

1. restate the user problem in LCTK terms;
2. identify the accepted LCTK boundary that owns the behavior;
3. define an LCTK-owned contract without copying upstream schemas;
4. add fixtures and acceptance tests;
5. implement the smallest vertical slice in idiomatic Go or through an independently selected specialized dependency;
6. record provenance in design documentation when the prior art materially influenced the design;
7. do not claim equivalent performance or quality without LCTK measurements;
8. do not introduce an upstream daemon, installer, updater, cache authority, or public API as a shortcut;
9. perform licensing review before copying any implementation material;
10. use an ADR if the change affects a long-term boundary.

## Candidate ideas for independent LCTK research

The following ideas merit later LCTK-owned research. This list is not a roadmap commitment:

- capability-specific index coverage reports;
- a compact repository architecture summary;
- supervised disposable indexing workers;
- SQLite-backed local graph storage;
- layered Tree-sitter breadth plus actual-LSP depth;
- cheap semantic candidate generation for low-resource mode;
- MinHash/LSH clone detection;
- graph-aware Git impact analysis;
- typed ADR memory as an initial memory category;
- resource backpressure and machine-wide indexing concurrency;
- deterministic generation-bound cursors;
- explicit negative-claim verification guidance for coding agents;
- graph quality benchmarks based on precision, recall, and unresolved-edge rates.

Each item requires its own priority, contract, and evidence.

## Suggested future benchmark dimensions

If LCTK evaluates its own graph or semantic implementation, it should avoid relying only on throughput. A useful benchmark should measure:

### Correctness

- definition precision and recall;
- call-edge precision and recall;
- import resolution;
- inheritance and implementation links;
- route-to-client linking;
- false positive and unresolved-edge rates;
- behavior on generated, vendored, and excluded code.

### Coverage

- files discovered;
- files intentionally excluded;
- files that failed reading;
- files with parser errors;
- partially extracted ranges;
- languages recognized versus deeply supported;
- constructs unsupported by a language adapter.

### Freshness

- save-to-query latency;
- branch checkout convergence;
- bulk change behavior;
- watcher overflow recovery;
- restart catch-up;
- graph and exact-index generation skew.

### Performance

- initial indexing time;
- peak and steady-state memory;
- CPU use;
- index size;
- warm and cold query latency;
- one-file update cost;
- cancellation latency;
- concurrent-project behavior on a 16 GB machine.

### Agent utility

- tool calls required for representative tasks;
- tokens returned, not only tokens requested;
- answer quality with source verification;
- negative-claim reliability;
- impact-analysis usefulness;
- behavior when coverage is partial or stale.

### Operations

- install size and time;
- startup latency;
- update and rollback;
- corrupted-state recovery;
- logs and diagnostics;
- Windows and macOS path behavior;
- offline operation;
- dependency and license posture.

## Risks of the accepted disposition

Using CBM only as prior art has costs:

- LCTK will reach advanced graph functionality later;
- LCTK must independently solve difficult language and graph problems;
- implementation may duplicate general work that exists elsewhere;
- users may prefer CBM's simpler immediate experience;
- a small project may not have enough contributors to achieve CBM's breadth;
- independent implementation can still converge on similar architecture without gaining upstream maintenance.

These risks are accepted because architecture ownership is a deliberate project goal. They must be managed by strict scope, small slices, explicit capability levels, and refusal to promise broad graph functionality before it is verified.

## Risks avoided by the accepted disposition

The decision avoids:

- coupling the LCTK public API to CBM tools;
- inheriting caller-selected project semantics;
- introducing a second daemon and lifecycle owner;
- depending on a pre-1.0 database format;
- carrying a large C subsystem or fork;
- increasing the core contributor barrier;
- importing a large vendored grammar and generated-data license surface through CBM;
- presenting CBM's heuristic language layer as actual LSP behavior;
- weakening route-bound scope for implementation convenience;
- making an upstream installer or updater part of LCTK's trust boundary.

## Final answer to "are we building this unnecessarily?"

The answer depends on which product is intended.

If the desired product were only:

```text
fast local structural search and graph exploration for an agent
```

then an existing product such as CBM would make a new implementation difficult to justify.

LCTK's accepted product is broader and defined by boundaries CBM does not provide:

```text
server-enforced project scope
+ stable client-independent MCP API
+ project lifecycle and isolation
+ persistent layered indexes
+ actual language services
+ constrained execution
+ uniform freshness and provenance
```

Therefore LCTK is not invalidated by CBM. The comparison instead creates a stricter obligation: LCTK must remain focused on its differentiating contracts and must not spend years recreating every attractive CBM feature before delivering a useful project-scoped vertical slice.

The accepted strategic position is:

> Learn from CBM's architecture and product evidence. Keep the LCTK core, wrapping, authority, contracts, and implementation independent. Deliver the LCTK-specific boundaries first, and add advanced code intelligence only through small, verified, LCTK-owned slices.

## Follow-up

- Keep this assessment linked from the documentation index.
- Apply ADR-0010 when considering CBM or a similar complete external product.
- Do not add a CBM integration spike to the roadmap.
- Continue the existing search-backend evaluation because CBM's live code search does not satisfy the LCTK persistent exact-search contract.
- Preserve actual-LSP requirements and distinguish them from heuristic graph inference.
- Add coverage semantics when AST and graph contracts are designed.
- Revisit repository-map priority after the first required vertical slice, using LCTK-owned contracts and implementation.
- Reassess operational footprint continuously against the one-binary user experience demonstrated by CBM.
