# Slice 1.4 Codex end-to-end: measured results

## What was measured

The [roadmap](../roadmap.md) scenario for Slice 1.4, driven against real
components by the tracked harness at
[`spikes/codex-end-to-end/`](../../spikes/codex-end-to-end/):

```text
register folder
→ start stack
→ connect project endpoint
→ project_info
→ attempt cross-project access and receive refusal/no data
→ stop and receive typed error
→ restart and reconnect
```

Reproduce with one command:

```bash
go run ./spikes/codex-end-to-end verify
```

## Artifacts

| Component | Version |
|---|---|
| LCTK | `0.1.0-dev`, built from the working tree |
| Codex binary | `0.146.0-alpha.9.2`, bundled in VS Code extension `openai.chatgpt` `26.727.40816-win32-x64` |
| Container runtime | Docker `29.5.3`, Linux containers |
| Host | Windows 10 Pro 22H2, amd64 |

The Codex artifacts are the same ones ADR-0012 is bound to, so this run extends
that contract rather than re-opening it.

## Method

Every run works inside a temporary directory with its own `LCTK_HOME` and its own
`CODEX_HOME`. The operator's real registry, grants, and Codex configuration are
neither read nor modified.

Real in this run: the `lctk` executable driven through its published commands, a
real `lctk daemon` subprocess serving the project route on a free port, real
containers with per-project networks, volumes, and read-only source mounts, the
Codex binary the VS Code extension itself runs, the configuration written by
`lctk codex config --apply`, and a credential delivered only in the environment
of a process the harness starts.

Nothing is simulated. Without a Linux-capable container runtime the harness skips
every step rather than reporting a hollow pass.

## Results

All fourteen steps passed, on two consecutive full runs.

| Step | Outcome |
|---|---|
| `register_folder` | **pass.** Two folders registered through `lctk project add`. |
| `start_stack` | **pass.** Both stacks reported `running` with a healthy container. |
| `daemon_listening` | **pass.** The real daemon served the project route. |
| `generate_client_config` | **pass.** Two `streamable_http` entries were written referencing `LCTK_TOKEN_ALPHA_…` and `LCTK_TOKEN_BETA_…`, and neither token appears anywhere in the file. |
| `client_accepts_config` | **pass.** `codex mcp get` parsed the generated entry and echoed the `streamable_http` transport. |
| `client_reports_reachable` | **pass.** `codex doctor --json` reported `mcp.config` `status: "ok"`, summary "MCP configuration is locally consistent", with two configured servers, none disabled, and no reachability failure. |
| `client_connects` | **pass.** The real client completed the handshake and reported `project_info` for the project server. |
| `project_info_through_client` | **pass.** The tool answered through Codex with the routed project. |
| `scope_survives_a_wrong_argument` | **pass.** The call carried `project_id` naming the *other* project plus `repository_root` and `path` of `/etc`. The answer was the routed project, with `scope_source: route_and_registry`, no trace of the other project, and no host path. |
| `cross_project_access_refused` | **pass.** A token issued for the first project was refused on the second project's route with `403 AUTH_FORBIDDEN`. |
| `stopped_project_is_typed` | **pass.** A stopped project returned `503` with the full envelope: `code: PROJECT_STOPPED`, `retryable: false`, `recommended_action: "Start it with lctk project start."`, plus `project_id` and `request_id`. |
| `stopped_reason_reaches_the_client` | **pass.** The typed body survived into the client's own error text, so a caller learns *why* rather than only that a transport failed. |
| `project_state_survives_restart` | **pass.** The project volume carried the container start counter from 1 to 2 across a full stop, so the stop released runtime resources without discarding project state. |
| `restart_reconnects` | **pass.** The same client session reached the project again after a stop and a start, with no change to the client configuration and no reload. |

### What the stopped-project error looks like to the client

The typed envelope reaches the client wrapped in the client's own transport
error, which reads roughly as:

> tool call failed … unexpected server response: HTTP 503: `{"error":{"code":"PROJECT_STOPPED", …}}`

This is worth stating precisely. The typed code, the `retryable` flag, and the
recommended action are all present and readable, but they arrive inside a
transport-error string rather than as structured fields the client surfaces on
its own. An agent can act on it; a client cannot render it as a first-class
typed error without parsing that string.

## Consequences for the design

- **The credential mechanism works against the real client.** A grant delivered
  only in a started process's environment is enough for Codex to authenticate,
  with no token in any file and no persistent change to the machine. This is the
  first evidence for [ADR-0014](../adr/0014-project-credential-delivery.md)
  beyond LCTK's own tests.
- **Route-bound scope holds through a real client.** The strongest single result
  is `scope_survives_a_wrong_argument`: a client-supplied project identifier and
  absolute paths did not move the answer. This is [ADR-0001](../adr/0001-route-bound-project-scope.md)
  verified from outside LCTK.
- **A stop is diagnosable, not silent.** Both the wire response and the client's
  surfaced error name `PROJECT_STOPPED`.
- **No reload is needed after a restart.** The stateless JSON endpoint from
  ADR-0012 means a project that comes back is simply reachable again.

## Limits of this evidence

- **The extension user interface was not exercised.** The harness drives the
  Codex binary directly. It is the same binary the extension runs, given the same
  generated configuration and the same credential delivery, but no result here
  covers the extension's own panels, indicators, or controls. ADR-0012 named this
  as a Slice 1.4 obligation and it is **not discharged**. The manual steps below
  close it.
- **One host, one platform.** Windows amd64 only. macOS is unverified for this
  boundary, as it was for Slice 0.4.
- **One Codex build, and it is an alpha.** A materially different Codex version
  requires re-running the harness.
- **The app-server protocol used to drive the client is experimental.** It is a
  verification driver only; no LCTK production code depends on it.
- **`startup_timeout_sec` and `tool_timeout_sec` enforcement remains unmeasured.**
  LCTK emits them only when a caller asks for them, so no unmeasured default is
  imposed.
- **The already-running-editor case is not covered here.** A new window from a
  running editor inherits that process's environment, so it does not receive the
  credential. `lctk codex launch` detects this where it can and refuses; the
  detection itself is verified only on Windows.

## Closing the user-interface gap by hand

These steps are what remains between this evidence and a complete Slice 1.4
claim. They require a human at the editor.

1. Close VS Code entirely.
2. `lctk project add PATH`, then `lctk project start PROJECT`.
3. `lctk daemon` in a terminal that stays open.
4. `lctk codex config --apply PROJECT`, and read the printed entry.
5. `lctk codex launch PROJECT`.
6. In the Codex panel, confirm the project server appears and lists
   `project_info`.
7. Ask the assistant to call `project_info`, and confirm it answers with the
   registered project and `scope_source: route_and_registry`.
8. `lctk project stop PROJECT`, ask again, and confirm the refusal names
   `PROJECT_STOPPED`.
9. `lctk project start PROJECT`, ask again, and confirm it answers without any
   configuration change.

Record the outcome in this document before Slice 1.4 is claimed complete in the
[roadmap](../roadmap.md).
