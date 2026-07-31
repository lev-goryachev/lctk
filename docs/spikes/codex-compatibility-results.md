# Codex compatibility results

## Status

Slice 0.4 measured result and accepted integration contract. The disposition was accepted on 2026-07-31; [ADR-0012](../adr/0012-codex-integration-contract.md) records the decision.

Measurement date: 2026-07-31.

The full scenario was executed. All six hard gates pass against the named artifacts. The tracked harness ran end to end on Windows with Go 1.26.5 and the real Codex CLI, and every step reported `PASS`:

```text
PASS harness_server_healthy
PASS codex_strict_config
PASS codex_doctor_mcp_reachable
PASS codex_mcp_handshake
PASS codex_mcp_reload
PASS codex_tool_call
PASS codex_typed_error
PASS route_bound_scope
```

Scope of the claim: one host, one Codex version, one run of the harness. This is hosted-test-equivalent evidence for the named artifacts, not a certified support statement. See [Limits of this evidence](#limits-of-this-evidence).

## Artifacts measured

| Artifact | Version | Role |
|---|---|---|
| VS Code extension `openai.chatgpt` | `26.727.40816-win32-x64` | the actual extension under test |
| CLI bundled in that extension | `codex-cli 0.146.0-alpha.9.2` | authoritative binary the extension runs |
| Official Codex MCP documentation | retrieved 2026-07-31 | intent and documented defaults |
| Host | Windows 10 Pro 19045, x86-64 | measurement environment |

The extension also bundles `rg.exe` (ripgrep 15.2.0), `codex-code-mode-host.exe`, `codex-command-runner.exe`, and `codex-windows-sandbox-setup.exe`.

Every measurement ran against an isolated `CODEX_HOME` under an ignored repository-local research directory. The operator's real Codex configuration was neither read nor modified. No measurement started a model turn, and no credentials were present for any run.

## Q1. Configuration path and schema

**Answer: MCP servers are discovered from the user-global `CODEX_HOME/config.toml` and, in a trusted project, from `.codex/config.toml` files walking up from the working directory to the project root. Project-local entries take precedence.** Verified.

Measured discovery behavior:

| Condition | Result |
|---|---|
| Project `.codex/config.toml`, no trust record | silently ignored; `config.load` reported `mcp servers: 1`, the global count |
| Same file after adding a project trust record | the project entry appeared alongside the global entry |
| Run from a nested subdirectory of a trusted repository | both the nested `.codex/config.toml` and the repository-root `.codex/config.toml` contributed servers |
| Explicit trust added for the nested directory as well | no change; trust at the repository root is sufficient |
| Same server name in global and project-root config | the project-root URL won |

The trust record is itself user-global:

```toml
[projects.'C:\work\example-repo']
trust_level = "trusted"
```

This resolves a documentation conflict. The official documentation states that project-scoped configuration applies to trusted projects only and that project-scoped values take precedence. A widely circulated third-party guide presents project-scoped configuration without the trust precondition; the first measurement in this spike reproduced that guide's assumption, found the project file ignored, and only produced the correct answer after the trust record was added. The measurement stands over the guide.

Rejection behavior is unforgiving and matters for generation:

- a single malformed key aborts the entire configuration load. A Windows path written as `[projects."C:\work\..."]` produced `missing escaped value` and **every** MCP server disappeared. Windows paths require a TOML literal string or escaped backslashes.
- when configuration fails to load, `codex doctor --json` drops the `mcp.config` check from the report entirely instead of reporting a failing MCP check. An operator diagnosing a missing server must read `config.load` first.
- `--strict-config` exists at the top level but is rejected for `codex mcp` subcommands: "`--strict-config` is not supported for `codex mcp`".

## Q2. Streamable HTTP fields

**Answer: Streamable HTTP is a first-class transport with a stable, verified field set.** Verified.

This configuration was accepted and echoed back by `codex mcp get <name> --json` with `transport.type = "streamable_http"`:

```toml
[mcp_servers.lctk_project]
url = "http://127.0.0.1:8123/projects/p1/mcp"
bearer_token_env_var = "LCTK_PROJECT_P1_TOKEN"
startup_timeout_sec = 20
tool_timeout_sec = 120
enabled = true

[mcp_servers.lctk_project.http_headers]
X-LCTK-Project = "p1"

[mcp_servers.lctk_project.env_http_headers]
X-LCTK-Token = "LCTK_TOKEN_ENV"
```

| Field | Meaning | Status |
|---|---|---|
| `url` | endpoint address | verified |
| `bearer_token_env_var` | name of the environment variable holding the token | verified |
| `http_headers` | map of header name to static value | verified |
| `env_http_headers` | map of header name to environment-variable name | verified |
| `enabled` | enable or disable without deleting the entry | verified |
| `startup_timeout_sec` | connection startup budget; documented default 10 | verified as accepted |
| `tool_timeout_sec` | tool execution budget; documented default 60 | verified as accepted |
| `enabled_tools`, `disabled_tools` | tool allowlist and denylist | reported by `mcp get`, not exercised |
| `auth` | documented default `oauth`; `chatgpt` for first-party servers | documented only |
| `oauth_resource`, `scopes` | OAuth parameters, also exposed as CLI flags | documented only |

`mcp get --json` additionally reports `auth_status`, observed as `"unsupported"` for a plain bearer-token server.

The CLI surface is `codex mcp add <NAME> --url <URL> [--bearer-token-env-var ENV] [--oauth-client-id ID] [--oauth-resource RES]`, plus `list`, `get`, `remove`, `login`, and `logout`. The `--env KEY=VALUE` flag is stdio-only and does not apply to HTTP servers.

Serde structures inside the binary also mention `startup_timeout_ms`, `environment_id`, `required`, `supports_parallel_tool_calls`, and `default_tools_approval_mode`. These were not exercised individually and are recorded as unverified.

Stale advice to avoid: `experimental_use_rmcp_client = true` combined with an inline `bearer_token`, which several third-party guides still recommend, does not describe this version.

## Q3. Bearer token supply

**Answer: a bearer token cannot be placed in configuration. It must come from an environment variable read by the Codex process, or from OAuth.** Verified.

An inline token is rejected by the loader:

```
config.toml:1:1: bearer_token is not supported for streamable_http
  in `mcp_servers.lctk_project`
```

The official documentation agrees that credentials must come from environment variables. Both `bearer_token_env_var` and `env_http_headers` carry variable **names**, so no committed file needs to contain a secret.

Consequences for LCTK:

- there is no key-helper or credential-command mechanism for MCP servers in this version. The only secret-free paths are an environment variable or OAuth.
- the variable is read by the Codex process, so it must be present in the environment that process inherits. The extension exposes no setting for `CODEX_HOME` or for MCP environment variables; the only related setting is `chatgpt.cliExecutable`. A per-workspace credential therefore cannot be injected through extension settings, and a newly created user-level variable is not visible to an already-running editor.
- `codex doctor --json` detects a missing variable and emits the remediation "Set the missing MCP env vars or disable the affected server". With both variables present and the endpoint live, the same check reported `status: "ok"` and "MCP configuration is locally consistent", so LCTK has a usable local diagnostic in both directions.
- OAuth would avoid the environment-variable problem but would make LCTK an OAuth authorization server for a local endpoint. That is a larger decision than Slice 0.4 should settle; it is recorded as an open question.

## Q4. Required server behavior

**Answer: verified.** The real Codex MCP client completes a full handshake against a route-bound LCTK-shaped endpoint, calls tools, and tolerates a stateless JSON server.

The harness journaled every exchange. Token values were never recorded; only the scheme and whether the presented token matched the route.

| # | Method | Status | Auth | Protocol version | Session | JSON-RPC |
|---|---|---|---|---|---|---|
| 1 | HEAD | 401 | none | – | no | – |
| 2 | POST | 200 | Bearer, matched | – | no | `initialize` |
| 3 | POST | 202 | Bearer, matched | `2025-06-18` | yes | `notifications/initialized` |
| 4 | GET | 405 | Bearer, matched | `2025-06-18` | yes | – |
| 5 | POST | 200 | Bearer, matched | `2025-06-18` | yes | `tools/list` |
| 6 | POST | 200 | Bearer, matched | `2025-06-18` | yes | `resources/list` |
| 7 | POST | 200 | Bearer, matched | `2025-06-18` | yes | `resources/templates/list` |
| 8 | DELETE | 204 | Bearer, matched | `2025-06-18` | yes | – |
| 9–14 | reconnect after reload | 200/202/405 | Bearer, matched | `2025-06-18` | new session | `initialize`, `notifications/initialized`, `tools/list`, `tools/call` ×2 |
| 15 | POST with a foreign token | 401 | Bearer, not matched | – | no | `tools/list` |

What this establishes:

- **Protocol version is `2025-06-18`**, sent as `Mcp-Protocol-Version` on every request after `initialize`.
- **`Authorization: Bearer` is derived from `bearer_token_env_var`.** The presented token matched the route's expected value exactly, so the environment variable reaches the wire unchanged.
- **Sessions are honored.** The client echoes the server-issued `Mcp-Session-Id` on every subsequent request and terminates the session with `DELETE`, which the server answered `204`.
- **Streaming is not required.** The client sends `Accept: text/event-stream, application/json`, but a stateless JSON server is sufficient: the `GET` stream attempt was answered `405` and the client continued normally through tool listing and tool calls.
- **Both header mechanisms work.** The static `http_headers` entry arrived as `X-Lctk-Project: lctk_alpha`, and the `env_http_headers` entry arrived with the value resolved from the named environment variable.
- **Route-bound scope holds through the real client.** `project_info` was invoked through `mcpServer/tool/call` with a model-supplied `project_id` of `lctk_beta`. The response contained only the `lctk_alpha` sentinel and no `lctk_beta` sentinel, so a model-supplied identifier does not change server-enforced scope. This satisfies [ADR-0001](../adr/0001-route-bound-project-scope.md) at the client boundary.
- **Typed errors survive.** The `typed_error` tool's `PROJECT_STOPPED` code was visible to the client, distinguishable from a transport failure.

Two user agents appear, which matters when writing local diagnostics: the reachability probe identifies as `codex_cli_rs/0.146.0-alpha.9.2`, while the MCP client identifies as `codex-mcp-client/0.146.0-alpha.9.2`.

One behavior deserves emphasis. **The `doctor` reachability probe sends no `Authorization` header.** Observation 1 was an unauthenticated `HEAD` that the harness answered `401`, and `doctor` still reported `status: "ok"` with the summary "MCP configuration is locally consistent". A `401` therefore counts as reachable, so an LCTK endpoint must not need to accept anonymous requests in order to look healthy, and must not treat the probe as a failed grant attempt worth alerting on.

The experimental app-server protocol, obtained from `codex app-server generate-json-schema --experimental` (47 schema files), provides the control surface the harness uses:

| Method | Params | Use |
|---|---|---|
| `mcpServerStatus/list` | `{cursor?, detail?, limit?, threadId?}` | inventory and per-server status; `threadId` optional |
| `mcpServer/tool/call` | `{server, tool, threadId, arguments?, _meta?}` | direct tool invocation; `threadId` required |
| `config/mcpServer/reload` | `null` | reload configured servers |
| `mcpServer/oauth/login` | login params | OAuth flow |
| `mcpServer/resource/read` | resource params | resource reads |

`initialize` succeeds with no credentials present and returns `userAgent`, `codexHome`, `platformFamily`, and `platformOs`. `thread/start` also works with no credentials; it returns the thread identifier at `thread.id` and reports the resolved `cwd`, workspace roots, and discovered instruction sources. A thread is required for `mcpServer/tool/call` but does not start a model turn, so the whole scenario runs without consuming quota.

A practical trap for anyone extending the harness: the `thread/start` result contains several unrelated `id` fields, including `activePermissionProfile.id` whose value is `":read-only"`. Searching the response generically for an `id` yields an invalid thread id and the tool call fails with `invalid thread id`. The identifier must be read from `thread.id`.

## Q5. Reload and reconnect UX

**Answer: `config/mcpServer/reload` works without restarting the editor, and it performs a full reconnect rather than an in-place refresh.** Verified.

The method was accepted with null parameters while the app server stayed alive. The journal shows what reload actually does: observations 9 to 14 are a complete new connection, beginning with a fresh `initialize` and a **new** `Mcp-Session-Id`, followed again by `notifications/initialized`, the `GET` stream attempt, and `tools/list`. The previous session had been terminated with `DELETE` at observation 8.

Consequences for LCTK:

- a server must tolerate repeated full handshakes and must not treat a new session as a new grant decision;
- per-session server state is discarded on reload, so nothing durable may live only in an MCP session;
- because reload re-runs `initialize`, a project that has become unreachable or stopped will surface at reconnect time, which is the natural place for a typed `PROJECT_STOPPED` response.

The official documentation additionally describes reconnection as automatic on configuration change and refers to an explicit **Restart** control in the desktop application. Whether a newly added or removed server is picked up by reload, and whether changed environment variables are re-read without restarting the process, was not measured.

## Q6. Generation and trust surface

**Answer: LCTK must generate two things in the user-global config, and may optionally generate a committable project file. Neither can carry the token.** Verified except where noted.

What LCTK must produce on the host:

1. an `mcp_servers` entry for each connectable project, in `CODEX_HOME/config.toml`, or a project trust record plus a project-local `.codex/config.toml`;
2. an environment variable carrying the project grant token, present in the environment the editor inherits.

Security-relevant findings that LCTK must account for:

- in a trusted repository, a checked-in `.codex/config.toml` at the repository root or in any ancestor directory of the working directory can register MCP servers, and can override the `url` of an LCTK-generated global entry with the same name. A repository author can therefore redirect a project endpoint name in a trusted checkout. LCTK must not rely on a global entry name being authoritative.
- trust is recorded in the user-global configuration and covers nested directories once granted at the repository root.
- Codex configuration is shared across the ChatGPT desktop app, the CLI, and the IDE extension, so an LCTK-generated entry is not scoped to one editor.
- a generated file must be escaped correctly, because one malformed key silently removes every MCP server from the loaded configuration.

The manifest boundary from the open questions is unaffected: the token stays out of every generated file, so a project-local `.codex/config.toml` is committable while the grant is not.

## Hard gate outcomes

| Gate | Outcome |
|---|---|
| 1. Streamable HTTP is usable | **pass.** The real client completed `initialize`, `notifications/initialized`, `tools/list`, and `tools/call` against `/projects/{project_id}/mcp`. |
| 2. No secret in a committed file | **pass.** Inline tokens are rejected outright, and both credential mechanisms reference environment-variable names. |
| 3. Route-bound scope survives the client | **pass.** A model-supplied `project_id` of `lctk_beta` did not change the answer, and a token issued for one project was refused on another project's route with a typed `GRANT_REQUIRED`. |
| 4. Typed errors survive | **pass.** `PROJECT_STOPPED` reached the client and was distinguishable from a transport failure. |
| 5. Reload without restarting the editor | **pass.** `config/mcpServer/reload` succeeded while the process stayed alive, performing a full reconnect with a new session. |
| 6. Unreachable projects are diagnosable locally | **pass.** `codex doctor --json` probes reachability and validates credential variables with no model turn, and reported `ok` against the live endpoint. |

No gate failed.

## Limits of this evidence

What was measured is bounded, and the contract must not be read more broadly than the run supports:

- **one host**: Windows 10 Pro 19045, x86-64, Go 1.26.5. macOS was not exercised, and hosted CI runs only the harness unit tests, because no Codex CLI exists on the runners.
- **one Codex version**: `codex-cli 0.146.0-alpha.9.2`, an alpha build bundled in extension `26.727.40816`. The app-server protocol used to drive it is explicitly experimental, so `mcpServerStatus/list`, `mcpServer/tool/call`, and `config/mcpServer/reload` may change shape without notice.
- **the CLI, not the editor UI**: the extension's own reload affordances, error presentation, and grant prompts were not exercised. The verification drove the same binary the extension runs, which is stronger than documentation but is not the same as clicking through VS Code.
- **one run, no repetition**: latency, flakiness under load, reconnect storms, and long-lived session behavior were not measured.
- **fields accepted but not exercised**: `enabled_tools`, `disabled_tools`, `auth`, `oauth_resource`, `scopes`, `startup_timeout_ms`, `environment_id`, `required`, `supports_parallel_tool_calls`, and `default_tools_approval_mode`.
- **timeout semantics not probed**: `startup_timeout_sec` and `tool_timeout_sec` were accepted by the loader, but no slow or hanging server was used to observe enforcement.
- **not measured for reload**: whether a newly added or removed server is picked up, and whether changed environment variables are re-read without restarting the process.

The harness is reproducible, so re-measurement is cheap:

```bash
go run ./spikes/codex-compatibility verify --out .research/codex-compat/report.json
```

## Accepted disposition

For the named artifacts, Codex is a suitable client for the LCTK project endpoint, and the integration is buildable without weakening route-bound scope or putting secrets in committed files. The contract accepted in [ADR-0012](../adr/0012-codex-integration-contract.md) is:

1. LCTK targets Streamable HTTP with `url` plus `bearer_token_env_var`, and does not use inline credentials.
2. LCTK owns the generated `mcp_servers` entry and treats Codex configuration as a generated artifact, never a source of truth for project identity.
3. LCTK never trusts a project identifier supplied by the client or by a repository-local Codex file. The authoritative project identity remains the route, per [ADR-0001](../adr/0001-route-bound-project-scope.md).
4. LCTK provides a local diagnostic path built on `codex doctor --json` semantics rather than inventing its own reachability story.
5. The credential-delivery mechanism, specifically how a project token reaches the editor's environment, is the main remaining design problem and needs its own decision.

Additional contract items now supported by measurement:

6. LCTK targets MCP protocol version `2025-06-18` and must tolerate repeated full handshakes, because reload reconnects rather than refreshes.
7. LCTK does not need server-initiated streaming for Codex. A stateless JSON endpoint that answers `405` to the `GET` stream is sufficient, which keeps the Slice 1.3 gateway simpler.
8. LCTK must answer an unauthenticated `HEAD` and `GET` on a project route with a typed `401` rather than a connection failure, so that `codex doctor` reports the project as reachable.
9. No durable state may live only in an MCP session, because reload discards it.

The remaining open item is credential delivery, not protocol compatibility.

## Follow-up

- Decide how a project grant token reaches the editor environment, given that extension settings cannot inject it and a new user-level variable is invisible to a running editor.
- Decide whether LCTK generates a user-global entry, a trusted project-local file, or both, in light of the project-local override finding.
- Decide whether a local OAuth path is worth avoiding environment variables.
- Exercise the extension UI itself before Slice 1.5 claims an end-to-end Codex scenario.
- Measure `startup_timeout_sec` and `tool_timeout_sec` enforcement against a deliberately slow server when the real gateway exists.
- Re-measure when Codex changes materially; this contract is bound to `codex-cli 0.146.0-alpha.9.2` and to an experimental app-server protocol.
