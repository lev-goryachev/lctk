# ADR-0003: Reusable images and isolated project stacks

- Status: accepted
- Date: 2026-07-24
- Deciders: project maintainers
- Amended by: [ADR-0023](0023-managed-podman-wsl-runtime-and-windows-installer.md) for the Windows runtime and Compose replacement

## Context

Projects require separate mounts, indexes, and lifecycles, but building a unique full image for every repository is expensive and complicates updates.

## Decision

LCTK publishes versioned reusable images. Each registered project receives a separate Docker Compose project or equivalent namespace with its own mounts, network, and volumes.

Specialized runner environments are provided through image profiles (Node, Python, Rust, C/C++, and others), rather than by copying the entire platform. A unique project runner image is permitted only for genuine environment dependencies.

## Alternatives considered

- **One shared runtime for all projects.** Saves resources but increases the risk of mixing state and complicates lifecycle management.
- **Unique images for every project.** Provides customization but makes rebuilds and updates too expensive.
- **Entirely host-native tooling.** Simplifies some integrations but reduces reproducibility and dependency isolation.

## Consequences

### Positive

- Project state and lifecycles are separated.
- Images are cached and updated centrally.
- Stop/start preserves indexes in project volumes.

### Negative

- Multiple active projects consume additional resources.
- A resource planner and support for shared stateless compute, such as one embedding inference process, are required.

### Follow-up

- Define Compose resource naming; release and image versioning follow [ADR-0007](0007-unified-versioning.md).
- Specify volume migration and purge semantics.
- Measure resource overhead on a 16 GB CPU-only machine.
