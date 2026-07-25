# ADR-0002: Host daemon and containerized project runtime

- Status: accepted
- Date: 2026-07-24
- Deciders: project maintainers

## Context

LCTK must register arbitrary folders on Windows and macOS, watch the host filesystem, and manage Docker Desktop. Giving the Docker socket to the gateway or coding agent grants disproportionate privileges. A fully containerized controller also handles native paths and file events on Docker Desktop bind mounts poorly.

## Decision

Distribute one `lctk` executable containing the host daemon and CLI command families. This amends the original separate CLI naming without changing the daemon boundary.

The daemon is responsible for host paths, the registry and secrets, the filesystem watcher, resource planning, the Admin UI, and the Docker lifecycle. The gateway and project code services run in containers and do not receive the Docker socket. The daemon provides a narrow local management API that is not available through the regular project coding endpoint.

Docker Desktop is the first supported runtime. Runtime integration is designed behind an interface, but Podman and Linux are not acceptance criteria for the first release.

## Alternatives considered

- **Entirely native control plane.** Simplifies paths but reduces the reproducibility of service dependencies.
- **Containerized controller with the Docker socket.** Simplifies deployment but creates an excessive privilege boundary and causes problems with host paths and watchers.
- **Manual Compose commands only.** Does not support the one-command UX, on-demand startup, or Admin UI.

## Consequences

### Positive

- Reliable watching of the native filesystem.
- The Docker daemon is not accessible to coding tools.
- Provides a foundation for the on-demand lifecycle and system notifications.

### Negative

- Signed and versioned host builds are required for Windows and macOS.
- A stable protocol is required between the daemon and containerized control plane.
- Installation and upgrades must coordinate the binary, schemas, and images.

### Follow-up

- Implement the Go daemon and CLI structure selected in [ADR-0006](0006-go-language-and-toolchain.md).
- Design the local management transport and permissions.
- Test Docker Desktop path sharing on Windows drives and macOS folders.
