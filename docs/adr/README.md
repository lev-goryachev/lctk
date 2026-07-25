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
| [0002](0002-host-daemon-and-container-runtime.md) | The host daemon manages lifecycle while services remain containerized | accepted |
| [0003](0003-reusable-images-and-project-stacks.md) | Reusable images and separate project stacks | accepted |
| [0004](0004-stable-aggregated-tool-api.md) | Stable aggregated MCP tool API | accepted |
| [0005](0005-host-watcher-and-incremental-indexing.md) | Host watcher and incremental indexing | accepted |
| [0006](0006-go-language-and-toolchain.md) | Go language and toolchain for LCTK-owned code | accepted |
| [0007](0007-unified-versioning.md) | Unified product versioning | accepted |
| [0008](0008-platform-and-ci-baseline.md) | Platform targets and hosted CI baseline | accepted |

Create a new ADR by copying [`template.md`](template.md). A number is not reused even if the ADR is later rejected.
