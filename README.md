# Local Code ToolKit (LCTK)

> **Status:** public pre-alpha. Project registration, per-project containers, route-bound project MCP endpoints, persistent `exact_search`, the client end-to-end lifecycle, and the host change journal are implemented and measured against real components. Incremental indexing driven by that journal, symbol and semantic intelligence, and safe command execution remain planned work. See the [roadmap](docs/roadmap.md) for what each slice claims and how it was verified.

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

The project is then reachable at `http://127.0.0.1:4444/projects/{project_id}/mcp`, serving two tools:

- `project_info` — what this endpoint is bound to, what it can do, and how fresh its index is;
- `exact_search` — indexed literal and regular-expression search over the saved working tree, including files that are saved but not committed.

Around that:

- `lctk daemon` hosts the gateway, the per-project grants, and the filesystem watcher;
- `lctk project status/stop/restart/remove/reindex/watch` and `lctk grant`, `lctk image`, `lctk settings`, `lctk doctor` for the rest of the lifecycle;
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
- [delivery roadmap](docs/roadmap.md);
- [Codebase Memory MCP comparative assessment](docs/spikes/codebase-memory-mcp-assessment.md);
- [Codex verification contract](docs/spikes/codex-compatibility.md) and [measured results](docs/spikes/codex-compatibility-results.md);
- [search backend evaluation](docs/spikes/search-backend-evaluation.md) and [measured results](docs/spikes/search-backend-evaluation-results.md);
- [open questions](docs/open-questions.md);
- [architecture decisions](docs/adr/README.md).

## Licensing and contributions

LCTK is licensed under the [Apache License 2.0](LICENSE). See [NOTICE](NOTICE) and [Third-Party Notices](THIRD_PARTY_NOTICES.md).

The license permits independent use and forks. External pull requests remain closed until roadmap Slice 1.5; see [CONTRIBUTING.md](CONTRIBUTING.md).
