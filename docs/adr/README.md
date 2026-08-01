# Architecture Decision Records

ADRs record significant decisions together with their context, alternatives, and consequences.

## Statuses

- `proposed` — under discussion;
- `accepted` — accepted;
- `rejected` — rejected;
- `superseded` — replaced by a newer ADR.

## Index

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-route-bound-project-scope.md) | Project scope is bound to the route and server-side context | accepted |
| [0002](0002-host-daemon-and-container-runtime.md) | The host daemon manages lifecycle while services remain containerized | superseded by ADR-0009 |
| [0003](0003-reusable-images-and-project-stacks.md) | Reusable images and separate project stacks | accepted |
| [0004](0004-stable-aggregated-tool-api.md) | Stable aggregated MCP tool API | accepted |
| [0005](0005-host-watcher-and-incremental-indexing.md) | Host watcher and incremental indexing | accepted |
| [0006](0006-go-language-and-toolchain.md) | Go language and toolchain for LCTK-owned code | accepted |
| [0007](0007-unified-versioning.md) | Unified product versioning | accepted |
| [0008](0008-platform-and-ci-baseline.md) | Platform targets and hosted CI baseline | accepted |
| [0009](0009-embedded-go-gateway-and-project-runtime.md) | LCTK-owned Go gateway embedded in the host daemon with containerized project services | accepted |
| [0010](0010-codebase-memory-mcp-reference-only.md) | Codebase Memory MCP is reference-only prior art | accepted |
| [0011](0011-zoekt-exact-search-backend.md) | Zoekt exact-search backend behind an LCTK adapter | accepted |
| [0012](0012-codex-integration-contract.md) | Codex integration contract measured against the real client | accepted |
| [0013](0013-registry-persistence.md) | Versioned JSON document for the local project registry | accepted |
| [0014](0014-project-credential-delivery.md) | Project credential delivery to a local client | accepted |
| [0015](0015-change-observation-is-complete-or-declared-incomplete.md) | Change observation is complete or declared incomplete | accepted |

Create a new ADR by copying [`template.md`](template.md). A number is not reused even if the ADR is later rejected.
