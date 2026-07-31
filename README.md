# Local Code ToolKit (LCTK)

> **Status:** public pre-alpha. Slice 0.1 provides a working, tested Go foundation. Project registration, persistent `exact_search`, route-bound project servers, and the Codex end-to-end lifecycle remain planned work.

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

## Current foundation

The current executable provides:

- `lctk version` for build metadata;
- `lctk daemon` with `GET /health` and a Streamable HTTP endpoint at `/mcp`;
- `foundation_info`, a temporary Slice 0.1 MCP compatibility tool rather than the future stable project API;
- `lctk watch-once`, an `fsnotify` event-delivery proof rather than the complete persistent change journal;
- `lctk doctor`, a read-only Moby API diagnostic for Docker Desktop availability.

Automated tests exercise the CLI, health handler, MCP transport/tool call, and watcher proof. CI builds and tests on hosted Windows and macOS runners. A manually triggered workflow constructs non-publishing Windows amd64 and Darwin arm64 archives plus `SHA256SUMS`.

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
- [search backend evaluation](docs/spikes/search-backend-evaluation.md) and [measured results](docs/spikes/search-backend-evaluation-results.md);
- [open questions](docs/open-questions.md);
- [architecture decisions](docs/adr/README.md).

## Licensing and contributions

LCTK is licensed under the [Apache License 2.0](LICENSE). See [NOTICE](NOTICE) and [Third-Party Notices](THIRD_PARTY_NOTICES.md).

The license permits independent use and forks. External pull requests remain closed until roadmap Slice 1.5; see [CONTRIBUTING.md](CONTRIBUTING.md).
