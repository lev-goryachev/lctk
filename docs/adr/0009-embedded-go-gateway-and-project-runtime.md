# ADR-0009: Embedded Go gateway and containerized project runtime

- Status: accepted
- Date: 2026-07-25
- Deciders: project maintainers
- Amended by: [ADR-0023](0023-managed-podman-wsl-runtime-and-windows-installer.md) for the managed Windows runtime and installer lifecycle
- Supersedes: [ADR-0002](0002-host-daemon-and-container-runtime.md)

## Context

LCTK needs a continuously available localhost MCP control plane that enforces route-bound project scope, validates client grants before dispatch, translates lifecycle failures into stable typed errors, and forwards requests to isolated project services. The host daemon already owns the authoritative project registry, grants, secrets, health state, and Docker lifecycle.

Slice 0.2 evaluated IBM ContextForge, Docker MCP Gateway, MCPJungle, and a minimal LCTK-owned Go gateway against the same alpha/beta scenario. The measured results are recorded in [`../spikes/gateway-evaluation-results.md`](../spikes/gateway-evaluation-results.md).

The external candidates provide useful gateway features, but none matches the complete LCTK boundary without retaining another registry or requiring an authoritative LCTK façade. The LCTK-owned proof directly enforced the stable project route and grant agreement, stripped the external credential before forwarding, returned the provisional typed failure envelope, supported live registration, and had the smallest measured footprint.

ADR-0002 selected one host executable and a containerized project runtime, but placed the gateway in a container. The evidence now supports embedding the shared gateway in the host daemon instead. Project code-intelligence and runner services remain containerized and isolated.

## Decision

Implement the shared MCP gateway as an LCTK-owned Go component inside the single `lctk` executable.

- The host daemon serves the stable Streamable HTTP route `/projects/{project_id}/mcp` on loopback.
- The daemon-owned persistent registry is the only authoritative project, route, grant, health, and upstream mapping store. The gateway does not introduce another authoritative database.
- The route project ID and validated client grant must agree before an MCP request is dispatched.
- Model-supplied project IDs, roots, paths, or other scope-like arguments never select or expand project scope.
- External client credentials are terminated at the gateway and are not forwarded to project services. Internal service authentication, if required, uses daemon-managed credentials that are separate from client grants.
- The gateway proxies to the registered project-local aggregated MCP service and preserves the stable LCTK tool API.
- The public boundary returns stable typed authentication, project-state, routing, timeout, and upstream-availability failures with request and project context where applicable.
- Dynamic project registration and removal must not require restarting the daemon.
- The MCP gateway code must not expose daemon administration or Docker lifecycle operations as regular coding tools. Project services receive neither the Docker socket nor daemon administrative credentials.
- Production code will be implemented behind explicit registry, grant-validation, health-resolution, error-translation, and upstream-transport interfaces. The Slice 0.2 proof package is evidence and is not imported as production code.

This decision changes only the gateway placement from ADR-0002. Docker Desktop remains the first runtime, and per-project code-intelligence and runner services remain containerized with isolated mounts, networks, indexes, memory, and volumes.

## Alternatives considered

- **MCPJungle.** The strongest external alternative and operationally lightweight. Not selected because LCTK would still need to own the stable route, lifecycle error translation, and authoritative host registry while synchronizing a second server/group/client administration model.
- **IBM ContextForge.** Verified native virtual-server scope and server-scoped JWT enforcement. Not selected because generated route identifiers, high measured footprint, readiness behavior, tool naming, extensive configuration, and the broader administration model require a substantial LCTK adapter and duplicated control state.
- **Docker MCP Gateway.** Static remote-only mode can run without the Docker socket and forwards efficiently. Not selected because one shared `/mcp` route exposes all enabled projects and one bearer-token surface does not provide LCTK project-route grant semantics.
- **Separate LCTK gateway container built from the same Go source.** Preserves process isolation but requires a host-to-container registry, grant, secret, health, and lifecycle synchronization protocol for a component whose authoritative state already lives in the daemon. This may be reconsidered only if a measured security or reliability requirement justifies the extra boundary.

## Consequences

### Positive

- Project scope, client grants, lifecycle state, and routing share one authoritative host-side state model.
- The public route and typed error contract do not depend on an external gateway product.
- Installation keeps one native executable and avoids another always-running control-plane container, database, migration stream, and release dependency.
- The gateway can resolve on-demand lifecycle state without synchronizing a second control plane.
- Project services remain isolated and do not receive Docker administration privileges or external client grants.

### Negative

- LCTK owns MCP proxy correctness, credential termination, protocol compatibility, backpressure, timeout handling, observability, and security maintenance.
- The daemon process also hosts the public localhost MCP boundary, increasing the importance of strict package interfaces and preventing coding routes from reaching administrative capabilities.
- Gateway failures can affect daemon availability unless handlers use bounded resources, panic containment, cancellation, and graceful draining.
- A future multi-process gateway would require a new ADR and a secure state-synchronization protocol.

### Follow-up

- Deliver Slice 1.3 through production interfaces rather than importing `spikes/gateway-evaluation`.
- Define and test the accepted typed error schema for `PROJECT_NOT_FOUND`, `PROJECT_STOPPED`, `PROJECT_STARTING`, authentication failures, and upstream availability.
- Add tests proving route/grant agreement, malicious scope arguments, credential stripping, dynamic registration, bounded forwarding, and graceful shutdown.
- Keep Docker lifecycle and Admin API handlers outside the project MCP capability surface even though they share the `lctk` process.
- Verify the generated Codex configuration and reconnect behavior in Slice 0.4 and the full two-project flow in Slice 1.5.
