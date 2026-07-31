# ADR-0010: Codebase Memory MCP is reference-only prior art

- Status: accepted
- Date: 2026-07-26
- Deciders: project maintainers

## Context

[Codebase Memory MCP](https://github.com/DeusData/codebase-memory-mcp), abbreviated CBM, is an active local code-intelligence product with substantial overlap with later LCTK goals. Its documented capabilities include broad Tree-sitter parsing, persistent graph storage, structural and semantic search, call-path traversal, architecture summaries, change-impact analysis, incremental refresh, local daemon coordination, an optional graph UI, and extensive client installation support.

The detailed source-based comparison is recorded in [`../spikes/codebase-memory-mcp-assessment.md`](../spikes/codebase-memory-mcp-assessment.md). The comparison found valuable architectural prior art, especially around graph-oriented agent answers, coverage reporting, supervised indexing workers, resource budgets, layered parsing, SQLite persistence, and operational simplicity.

It also found fundamental boundary differences:

- CBM's public MCP model is primarily stdio rather than LCTK's project-routed Streamable HTTP contract.
- CBM uses account-scoped native coordination and caller-visible project selection rather than route-bound project identity and per-client project grants.
- CBM's native process and per-project database separation is not LCTK's project runtime and runner boundary.
- CBM's grep-like raw code search is not LCTK's required persistent exact/regex index.
- CBM's internal "Hybrid LSP" static inference is not an actual language-server integration.
- CBM does not provide the planned LCTK constrained build/test/lint runner.
- CBM's pre-1.0 tools, database, daemon, and release lifecycle would become additional compatibility and trust boundaries if adopted.
- Adopting or forking a large C engine would raise the core contribution and maintenance barrier despite its strong single-binary user experience.

An initial analysis considered CBM as an optional wrapped or containerized graph backend. The maintainer rejected that direction. LCTK is intended to own its core architecture and operational wrapping rather than become a façade over CBM or another complete external code-intelligence product.

This decision distinguishes a complete external product from a specialized implementation dependency. LCTK may still select libraries, language servers, search engines, container runtimes, or other bounded tools through explicit evaluations. Such dependencies remain replaceable implementation details behind LCTK-owned contracts. They do not own the LCTK project registry, grants, public API, lifecycle, policy, or control plane.

## Decision

Treat CBM as reference-only public prior art.

LCTK may study and independently validate CBM's architectural ideas, product trade-offs, tests, and user-experience lessons. LCTK will not:

- adopt CBM as its core or control plane;
- execute a CBM binary as a production backend;
- create a CBM adapter, wrapper, compatibility mode, or container integration;
- vendor or fork CBM source;
- copy CBM tool schemas, daemon protocol, database format, generated semantic data, bundled grammar set, installers, hooks, or update mechanism;
- expose CBM behavior as the LCTK public compatibility contract;
- make CBM's account-wide project model part of an LCTK project endpoint.

The following remain LCTK-owned and are implemented, versioned, and tested in the LCTK architecture:

- host daemon and CLI;
- registry and canonical project binding;
- client grants and capability policy;
- embedded Streamable HTTP gateway;
- stable public MCP routes, tools, errors, and normalized responses;
- lifecycle and health aggregation;
- watcher normalization, change journal, and freshness model;
- backend orchestration and adapters;
- manifest and project policy;
- container lifecycle integration;
- constrained runner boundary;
- Admin API authorization;
- diagnostics and audit contracts.

Go remains the language for LCTK-owned code under ADR-0006. General ideas learned from CBM may be implemented independently in idiomatic Go only when they satisfy an accepted LCTK requirement and have LCTK-owned tests and documentation.

Specialized third-party libraries and tools are not prohibited categorically. They require an explicit bounded contract, evaluation, version and license policy, and replacement strategy. No such dependency may become an additional authoritative registry, grant system, public API owner, lifecycle control plane, or project-scope authority.

## Alternatives considered

### Stop LCTK and recommend CBM

Rejected. This would be reasonable for a product limited to local graph exploration, but it would abandon accepted LCTK requirements for route-bound scope, client grants, Streamable HTTP, actual language servers, constrained execution, and uniform freshness and provenance.

### Adopt CBM wholesale

Rejected. It would accelerate advanced code intelligence but make CBM's transport, project model, daemon, database, tools, update lifecycle, and C implementation part of LCTK's defining architecture.

### Run CBM behind an LCTK adapter

Rejected explicitly. Although an adapter could hide CBM's public schemas and enforce a project mount, LCTK would still depend on CBM's executable, persistence, migrations, behavior, resource model, and release cadence. The resulting product would contain two operational cores.

### Vendor or maintain an LCTK fork

Rejected. A fork would impose a large C maintenance burden, complicate security updates and notices, reduce contributor accessibility, and duplicate rapid upstream development.

### Copy selected implementation code or generated data

Rejected. Architectural patterns may be studied, but implementation material, generated vectors, bundled assets, and storage schemas are outside the accepted reference-only relationship.

### Ignore CBM entirely

Rejected. It is relevant prior art and contains useful lessons. Refusing to study it would increase the risk of avoidable design mistakes and uncompetitive operational complexity.

## Consequences

### Positive

- LCTK retains authority over project scope, grants, lifecycle, policy, and public compatibility.
- The core remains Go-based and accessible to the intended contributor population.
- No large C subsystem, CBM fork, or second daemon is introduced.
- LCTK is not tied to CBM's pre-1.0 schemas, database migrations, installer, or updater.
- CBM's strongest ideas can still inform independent LCTK designs.
- Search, language, graph, and semantic capabilities can be evaluated against LCTK-specific contracts rather than inherited product assumptions.
- Licensing and supply-chain scope do not expand merely because CBM was studied.
- The distinction between actual LSP behavior and heuristic graph inference remains explicit.

### Negative

- LCTK cannot use CBM to accelerate graph, semantic, architecture-map, or broad-language delivery.
- Difficult graph and language-intelligence work may need independent implementation or separately evaluated bounded dependencies.
- Advanced capabilities will arrive later.
- LCTK risks duplicating general engineering already demonstrated elsewhere.
- The project must remain disciplined enough not to recreate CBM's entire feature set before delivering its differentiating vertical slices.
- A heavier containerized architecture will be compared against CBM's simpler single-binary experience.

### Follow-up

- Keep the comparative assessment as the detailed evidence record.
- Do not add a CBM integration spike or backend adapter to the roadmap.
- Continue Slice 0.3 because CBM does not satisfy the accepted persistent exact-search contract.
- Preserve the actual language-server requirement when graph and AST work begins.
- Add capability-specific coverage semantics to future AST and graph designs.
- Use CBM's operational simplicity as a benchmark when deciding whether an LCTK component needs a separate service.
- Require a new superseding ADR before any future CBM source, binary, protocol, database, generated data, or production integration is introduced.
