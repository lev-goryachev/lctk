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

All fourteen Slice 1.4 steps passed, on two consecutive full runs. The steps added
later, covering every remaining tool, are reported
[below](#every-tool-called-rather-than-listed).

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

## Every tool, called rather than listed

The original run verified `project_info` and left the other tools discovered but
never invoked. That is a weaker result than it looks: appearing in `tools/list`
proves the server *described* a tool, not that a client can send it arguments and
read the answer back. Each tool carries an input schema, and a schema is where a
client and a server disagree.

`exact_search` had never been called through a second client at all, and
`git_status`, `git_diff`, and `run_command` had been measured only by hand-driven
JSON-RPC against the endpoint. The harness was therefore extended rather than a
second one written, and now calls everything the endpoint offers.

Its first project is prepared as a real repository: one commit behind it, an
uncommitted edit in front of it, and a manifest proposing `lint` and `test`, of
which only `lint` is approved. The runner image is the one this repository builds,
so no external image is assumed and the command runs in a real container.

| Step | Outcome |
|---|---|
| `approve_a_command` | **pass.** `lint` approved for the project in `lctk/code-intel:0.1.0-dev`. |
| `client_connects` | **pass.** The client discovered all five tools: `exact_search`, `git_diff`, `git_status`, `project_info`, `run_command`. |
| `exact_search_through_client` | **pass.** A line that was **saved and never committed** was found through the client. This is the claim the whole indexing design exists to make, checked from outside LCTK: the index describes the working tree, not the last commit. |
| `bad_pattern_refused_visibly` | **pass.** An uncompilable regular expression produced `INVALID_PATTERN: the regular expression is invalid: error parsing regexp: missing closing ]`, not an empty result set — which would have read as "no such code in this project" and sent an agent looking elsewhere. |
| `invented_argument_refused` | **pass.** An undeclared argument was refused by schema validation before any handler saw it: `unexpected additional properties ["invented_filter"]`. |
| `git_status_through_client` | **pass.** The uncommitted change was reported with the branch and commit, `root: /workspace`, and no host path. |
| `git_diff_through_client` | **pass.** A unified diff of the one named path came back through the client. |
| `escaping_path_refused_visibly` | **pass.** `../outside.txt` produced `INVALID_PATH: the path must stay inside the repository` in the client's own tool result, marked `isError`. |
| `approved_command_runs` | **pass.** The approved command ran in a container and its output reached the client. |
| `unapproved_command_refused` | **pass.** `COMMAND_NOT_APPROVED`, naming the command that fixes it. |
| `unproposed_command_refused` | **pass.** `COMMAND_NOT_PROPOSED`, naming the manifest key to add. |
| `unknown_command_refused` | **pass.** `COMMAND_UNKNOWN: LCTK runs only build, test, and lint.` |

The refusals are the point. Each calls for something different from whoever reads
it — approve this, add it to the manifest first, correct your expression, stop
asking for a command that does not exist — and all of them survive into the
client's own tool result, where an agent reads them and acts. A single generic
failure would have been protocol-correct and useless.

`invented_argument_refused` completes the scope guarantee from the other side. A
*declared* argument such as `project_id` is accepted and disregarded, which
[`scope_survives_a_wrong_argument`](#results) checks; an *undeclared* one fails
validation outright. Silently dropping it would leave an agent believing a filter
had been applied.

`run_command` also confirmed the boundary it exists to hold: the only command that
ran is the one a human had approved by name, and the manifest's `test` entry —
present, proposed, and deliberately unapproved — never reached a container.

## Second client: Claude Code

The harness drives one client. A protocol contract verified against a single
implementation is weak evidence that it is the protocol, and not that
implementation, that LCTK satisfies. The chain was therefore repeated by hand
against a second, unrelated MCP client — Claude Code — connected to the same live
endpoint serving this repository as a registered project.

| Check | Outcome |
|---|---|
| Handshake | **pass.** `initialize` returned protocol `2025-06-18` and `serverInfo.name` `lctk-project-<id>`; `tools/list` returned `project_info`. |
| Credential shape | **pass.** The client stored `Bearer ${LCTK_TOKEN_…}` — a variable reference, not a value — and resolved it at connect time. The ADR-0014 shape transferred to a client with an entirely different configuration format. |
| Connected | **pass.** The client's own health check reported the project server as connected. |
| Missing credential | **pass.** With the variable unset, the client refused to connect and named the missing variable: "Missing environment variables: `LCTK_TOKEN_…`". LCTK knows nothing about this client, yet the failure is self-explaining. |
| Cross-project refusal | **pass.** A second entry pointing at another project's route while carrying this project's token failed to connect, while the correctly paired entry connected in the same check. |
| Stopped project | **pass.** After `lctk project stop`, the client failed to connect and the wire response carried the full typed envelope: `PROJECT_STOPPED`, `retryable: false`, recommended action, `project_id`, `request_id`. |
| Restart | **pass.** After `lctk project start`, the same entry connected again with no configuration change. |
| **Agent tool call** | **pass.** A real agent session called `project_info` with `project_id` deliberately set to *another* registered project. The answer was the routed project, with `scope_source: route_and_registry`, `root: /workspace`, and no host path. |

The last row is the result worth stating plainly. Route-bound scope was not merely
asserted by a test harness; an actual coding agent tried to name a different
project and the endpoint answered for the one its route was bound to.

## Limits of this evidence

- **The Codex extension user interface was not exercised.** The harness drives
  the Codex binary directly. It is the same binary the extension runs, given the
  same generated configuration and the same credential delivery, and a second
  independent client now corroborates the endpoint — but no result here covers
  the extension's own panels, indicators, or controls. What is verified is the
  protocol boundary, not that particular editor's presentation of it. The manual
  steps below close the remaining gap for anyone who wants it closed.
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
