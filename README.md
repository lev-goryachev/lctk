# Local Code ToolKit (LCTK)

> **Status:** public pre-alpha. Project registration, route-bound MCP, persistent exact and semantic search, syntax and graph intelligence, explicit project memory, transactional bootstrap/update/rollback, and a fail-closed release pipeline are implemented. Exact support claims remain limited to the evidence in the [roadmap](docs/roadmap.md) and [compatibility matrix](docs/compatibility.md).

Local Code ToolKit is a local, extensible MCP platform for software development. It decouples code intelligence, indexing, project memory, and command execution from any specific LLM or IDE.

The official Codex extension for VS Code is the first target client. The public MCP API is designed to support other compatible agents later without changing internal indexes or services.

## Target properties

- a separate MCP endpoint and runtime state for each registered project;
- enforced project scope: the model does not select a project through tool-call arguments;
- local persistent exact, symbol, AST, semantic, and graph indexes;
- automatic incremental indexing after files are saved;
- a shared control plane and reusable container images;
- a separate project-scoped runner;
- local operation without mandatory cloud APIs after components are installed;
- a minimal local Admin UI and one `lctk` command family;
- Windows 10 22H2 x86-64 and macOS 13 arm64 as initial compatibility targets;
- Docker Desktop as the first container-runtime target.

Compatibility targets are not certified configurations yet. Hosted Windows and macOS CI provides build and test evidence on GitHub runner environments; it does not certify the exact target operating systems or Docker Desktop integration.

## What works today

```sh
lctk project add PATH          # register a folder; nothing is started
lctk project start PROJECT     # bring up its isolated container stack
lctk codex launch PROJECT      # start an editor with the project's grant in its environment
```

The project is then reachable at `http://127.0.0.1:4444/projects/{project_id}/mcp`, serving up to eighteen tools according to the project's live capabilities:

- `project_info` — what this endpoint is bound to, what it can do, and how fresh its index is;
- `exact_search` — indexed literal and regular-expression search over the saved working tree, including files that are saved but not committed;
- `git_status` and `git_diff` — what has changed since the last commit, read-only, for a client that has no shell on the machine;
- `run_command` — the project's build, test, or lint, but only the ones the machine owner approved, and only by name;
- `file_outline` — syntax-derived declarations and parse status for one file;
- `find_definition` and `find_references` — bounded, name-matched declaration and reference lookup across supported source files;
- `code_search_semantic` — local conceptual search with syntax-aware chunks, lexical/vector rank evidence, model identity, and exact-versus-semantic generation freshness;
- `callers_find` and `callees_find` — bounded name-matched call evidence with ambiguity and generation provenance;
- `dependency_path` and `impact_analyze` — syntax-import routes and direct reverse-import/call evidence without type-resolution claims;
- `repository_map` — deterministic importance-ranked declarations inside an exact character budget;
- `memory_get`, `memory_search`, `memory_put`, and `memory_delete` — explicit persistent project knowledge with optimistic revisions, provenance, review/confidence labels, Git commit awareness, and hybrid retrieval.

The index follows edits on its own. Saving a file makes it searchable without any command; measured on this repository, a file written was found by search 0.2 seconds later. When the index cannot be brought up to date, the answer says so rather than looking current.

Around that:

- `lctk daemon` hosts the gateway, the per-project grants, and the filesystem watcher;
- `lctk bootstrap` for a read-only installation plan and confirmed immutable model/inference installation with a functional self-test;
- `lctk update` for a signed read-only release plan, candidate project health gates, atomic host activation, and verified rollback;
- `lctk project status/stop/restart/remove/reindex/watch/resources` and `lctk grant`, `lctk image`, `lctk settings`, `lctk doctor` for the rest of the lifecycle;
- `lctk admin open` for a local page with the same operations, over an API a project credential cannot reach;
- the scope of a request comes from the route and the server-side registry, so a tool argument naming another project is ignored and a credential issued for one project is refused on another.

Automated tests cover the CLI, the gateway and its scope guarantees, the search adapter, the watcher, and the change journal. CI builds and tests on hosted Windows and macOS runners, and the containerized search service on Linux. Container-dependent tests run against real Docker on a developer machine and skip explicitly on hosted runners rather than being simulated.

## Documentation

The source of truth is under [`docs/`](docs/index.md):

- [product vision and scope](docs/product.md);
- [architecture](docs/architecture.md);
- [project lifecycle](docs/project-lifecycle.md);
- [indexing and freshness](docs/indexing.md);
- [trust model and security boundaries](docs/security.md);
- [compatibility targets and evidence](docs/compatibility.md);
- [development workflow](docs/development.md);
- [versioning](docs/versioning.md) and [release process](docs/releasing.md);
- [Stage 7 stress evidence](docs/stress.md);
- [delivery roadmap](docs/roadmap.md);
- [Codebase Memory MCP comparative assessment](docs/spikes/codebase-memory-mcp-assessment.md);
- [Codex verification contract](docs/spikes/codex-compatibility.md) and [measured results](docs/spikes/codex-compatibility-results.md);
- [search backend evaluation](docs/spikes/search-backend-evaluation.md) and [measured results](docs/spikes/search-backend-evaluation-results.md);
- [open questions](docs/open-questions.md);
- [architecture decisions](docs/adr/README.md).

## Licensing and contributions

LCTK is licensed under the [Apache License 2.0](LICENSE). See [NOTICE](NOTICE) and [Third-Party Notices](THIRD_PARTY_NOTICES.md).

The license permits independent use and forks. External pull requests remain closed until roadmap Slice 1.5; see [CONTRIBUTING.md](CONTRIBUTING.md).
