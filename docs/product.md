# Product vision

## Status

Working requirements document. Last updated: 2026-07-25.

LCTK is public pre-alpha software. Slice 0.1 implements the executable and repository foundation; the first required product vertical slice remains future work.

## Goal

Local Code ToolKit (LCTK) is durable local coding infrastructure exposed to agents through MCP. The model, agent, and IDE must be replaceable; indexing, search, the code graph, project memory, and runtime must not belong to a specific desktop client.

The primary user interface for the first release is VS Code with the official Codex extension. A large custom IDE extension is not planned. The CLI is used for bootstrap, diagnostics, automation, and lifecycle operations. A minimal local Admin UI provides routine operations without requiring users to remember commands.

## Target user

Initially, the system targets a single user on their own machine and trusted local repositories. The public project must be installable by other users, but multi-user tenancy, cloud SaaS, and a hostile-code sandbox are outside the first release.

## Accepted product requirements

### Local-first

- After the components are installed, exact search, semantic search, symbols, the graph, and project memory work without mandatory paid APIs or cloud accounts.
- Internet access may be required by Codex, installation, updates, package managers, and explicitly permitted project commands.
- There is no external telemetry. Logs, diagnostics, and reports remain local.

### Project scope

- Each explicitly registered root folder receives a stable local `project_id`.
- One `project_id` corresponds to one registered folder; nested monorepo packages remain within its scope.
- Each project receives a separate MCP endpoint, indexes, memory, runtime metadata, and volumes.
- The model does not select the authoritative `project_id` and cannot escape into another project by passing a path or ID in tool arguments.

### Complete capability set, incremental delivery

The target platform includes:

- indexed exact/regex search;
- LSP symbols, definitions, references, implementations, and diagnostics;
- AST and structural search;
- local semantic search;
- persistent code graph;
- a compact repository map;
- Git awareness;
- a constrained runner for tests, builds, and linting;
- project memory;
- freshness, provenance, and observability.

“Incremental” means small, complete vertical slices of the target architecture, not disposable stubs or deliberately dead-end backend choices.

### Project profiles

A new project receives the `full` profile by default. The profile determines enabled capabilities and policies, but does not create a unique platform or unique images for each project.

### User experience

The target bootstrap experience is a single command that:

1. checks prerequisites;
2. shows the required components and total download size;
3. allows the entire operation to be canceled before downloading;
4. installs host components;
5. downloads compatible images, models, and language tooling;
6. verifies functionality.

This screen does not offer partial installation of the required component set.

For project-specific build, test, and lint configuration, LCTK provides a typed schema, validation, and machine-readable discovery data. The coding agent helps the user create `.mcp-project.yaml`; LCTK validates and applies the manifest.

### Supported platforms

The initial compatibility targets are:

- Windows 10 22H2 on x86-64;
- macOS 13 on arm64;
- Docker Desktop as the initial container-runtime target.

These are target contracts, not certified configurations. Hosted CI runs on available GitHub Windows and macOS runner images and cannot certify the exact operating-system versions or Docker Desktop integration.

Linux is planned for later. The architecture must not deliberately block Linux or alternative OCI runtimes, but they are not required integration targets for the first release.

### Resource model

- Baseline machine: 16 GB RAM, CPU-only.
- The number of full projects that can run concurrently is not guaranteed as a fixed value: the resource planner evaluates the specific projects and warns the user.
- Up to one million files is the upper stress target, not the normal baseline.
- Multi-hour initial indexing is acceptable if it runs in the background, reports progress, and does not block capabilities that are already ready.
- The user selects a global process mode: `always-on` or `on-demand`.
- The user selects a background-load mode, such as quiet, normal, or fast.
- Disk usage is estimated before indexing and confirmed by the user.

## Out of scope for the first release

- a custom IDE or LLM;
- a custom vector database or LSP implementation;
- multi-user tenancy;
- cloud control plane and Kubernetes deployment;
- mandatory support for every MCP client;
- a complex OAuth provider;
- dozens of external integrations;
- an automatic hostile-repository sandbox;
- direct control of Docker by the coding agent.

## First required vertical slice

```text
register a project folder
→ start the project container
→ connect the project-scoped MCP endpoint
→ call project_info and exact_search from Codex
→ confirm that another project is inaccessible
→ stop the stack
→ start it again without losing persistent state
```

The vertical slice is considered complete only after an actual verification through a supported Codex configuration, not after an internal test simulates an MCP call.

The Slice 0.1 `/mcp` endpoint and `foundation_info` tool prove foundation-level Streamable HTTP compatibility only. They do not complete any project registration, scope isolation, persistent search, or Codex lifecycle requirement above.
