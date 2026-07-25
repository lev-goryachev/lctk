# ADR-0001: Route-bound project scope

- Status: accepted
- Date: 2026-07-24
- Deciders: project maintainers

## Context

A single LCTK instance serves multiple local projects. An agent may make a mistake or attempt to supply an arbitrary `project_id` or path. Project identity must survive an MCP reconnect and cannot be the same as the transport session ID.

## Decision

Each project receives an endpoint of the following form:

```text
/projects/{project_id}/mcp
```

The gateway extracts the authoritative scope from the route, validates the client grant, and obtains the canonical root from the local registry. Internal calls receive project context from the gateway. Scope-like model arguments are ignored or rejected; they never expand access.

## Alternatives considered

- **Pass `project_id` in every tool call.** Rejected: the model is not an authorization boundary.
- **Determine the project from the client's current directory.** Rejected: HTTP MCP has no reliable concept of the client CWD.
- **Use the MCP session ID.** Rejected: the session belongs to the transport lifecycle and may be recreated.
- **Use one shared endpoint for all projects.** Rejected as the primary coding UX because of the risk of mixing context.

## Consequences

### Positive

- Cross-project isolation is enforced at one server boundary.
- Tool schemas do not contain a mandatory tenant selector.
- Reconnecting does not change project identity.

### Negative

- Each project requires local client configuration.
- The gateway must support virtual project routing and grant validation.

### Follow-up

- Verify the official Codex MCP configuration schema.
- Add an end-to-end test with two projects and deliberately malicious path and ID arguments.
