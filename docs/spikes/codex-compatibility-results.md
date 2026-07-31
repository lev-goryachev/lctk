# Codex compatibility results

## Status

Slice 0.4 measured result and **proposed** integration contract. Not yet reviewed with the maintainer, and therefore not an accepted decision.

Measurement date: 2026-07-31.

Part of the scenario is **unverified**: the tracked harness has not been compiled or executed because no Go toolchain is installed on the measurement machine. The affected gates are listed in [Unverified](#unverified) and must not be treated as passing.

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
[projects.'D:\Projets\lctk\.research\codex-compat\repo-b']
trust_level = "trusted"
```

This resolves a documentation conflict. The official documentation states that project-scoped configuration applies to trusted projects only and that project-scoped values take precedence. A widely circulated third-party guide presents project-scoped configuration without the trust precondition; the first measurement in this spike reproduced that guide's assumption, found the project file ignored, and only produced the correct answer after the trust record was added. The measurement stands over the guide.

Rejection behavior is unforgiving and matters for generation:

- a single malformed key aborts the entire configuration load. A Windows path written as `[projects."D:\Projets\..."]` produced `missing escaped value` and **every** MCP server disappeared. Windows paths require a TOML literal string or escaped backslashes.
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
- `codex doctor --json` detects a missing variable and emits the remediation "Set the missing MCP env vars or disable the affected server", which gives LCTK a usable local diagnostic.
- OAuth would avoid the environment-variable problem but would make LCTK an OAuth authorization server for a local endpoint. That is a larger decision than Slice 0.4 should settle; it is recorded as an open question.

## Q4. Required server behavior

**Answer: partially unverified.** The client-side control surface is verified; the wire-level contract is not.

Verified: the Codex app server accepts `initialize` with no credentials present and returns `userAgent`, `codexHome`, `platformFamily`, and `platformOs`, followed by a `remoteControl/status/changed` notification. This establishes that the real client can be driven without an account, without quota, and without a model turn.

The experimental app-server protocol, obtained from `codex app-server generate-json-schema --experimental` (47 schema files), exposes exactly the control surface a harness needs:

| Method | Params | Use |
|---|---|---|
| `mcpServerStatus/list` | `{cursor?, detail?, limit?, threadId?}` | inventory and per-server status; `threadId` optional |
| `mcpServer/tool/call` | `{server, tool, threadId, arguments?, _meta?}` | direct tool invocation; `threadId` required |
| `config/mcpServer/reload` | `null` | reload configured servers |
| `mcpServer/oauth/login` | login params | OAuth flow |
| `mcpServer/resource/read` | resource params | resource reads |

`codex doctor --json` independently probes each configured HTTP endpoint with HEAD and then GET and reports failures as, for example, `lctk_project: http://127.0.0.1:8123/projects/<redacted> (HEAD connect failed; GET connect failed)`. Values are redacted in JSON output. The MCP check took about 4.1 s against an unreachable endpoint.

Not verified: what the client sends on the wire, whether a stateless JSON response is sufficient, whether typed errors survive, and whether route-bound refusal behaves correctly through the real client. See [Unverified](#unverified).

## Q5. Reload and reconnect UX

**Answer: a reload mechanism exists and does not require restarting the editor, but its observable behavior is unverified.** Partially verified.

`config/mcpServer/reload` is a first-class app-server method taking null parameters. The official documentation describes reconnection as automatic on configuration change and refers to an explicit **Restart** control in the desktop application. The extension exposes no MCP-specific command in its contributed settings.

Whether reload picks up a newly added server, drops a removed one, and re-reads environment variables without restarting the process was not measured.

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
| 1. Streamable HTTP is usable | **partially verified.** The transport, schema, and client control surface are verified; a completed handshake against a live LCTK-shaped endpoint is not. |
| 2. No secret in a committed file | **pass.** Inline tokens are rejected outright, and both credential mechanisms reference environment-variable names. |
| 3. Route-bound scope survives the client | **unverified.** Enforced by the harness server design, not yet exercised through the real client. |
| 4. Typed errors survive | **unverified.** |
| 5. Reload without restarting the editor | **partially verified.** The mechanism exists; its behavior was not observed. |
| 6. Unreachable projects are diagnosable locally | **pass.** `codex doctor --json` probes reachability and validates credential variables with no model turn. |

No gate failed. Three gates are incompletely measured.

## Unverified

The tracked harness in [`spikes/codex-compatibility/`](../../spikes/codex-compatibility/) has not been compiled or executed. No Go toolchain is installed on the measurement machine: `go` and `gofmt` are absent from `PATH`, and no installation was found under Program Files, `LOCALAPPDATA`, scoop, or chocolatey. The harness source is therefore committed as unvalidated code, and hosted CI has not yet run its tests.

Consequently the following are unmeasured and must not be claimed:

- the HTTP method, `Accept` header, protocol-version header, and session-id handling the real client uses;
- that the `Authorization: Bearer` header is actually derived from `bearer_token_env_var`;
- whether a stateless JSON response satisfies the client, or whether streaming and session support are required;
- whether `mcpServerStatus/list` returns the tool inventory from a live server;
- whether a typed server error is distinguishable from a transport failure;
- `config/mcpServer/reload` behavior after a configuration change;
- refusal of a foreign project token on another project's route as seen by the real client.

Completing these requires a Go toolchain on a machine that also has the Codex extension installed, then running the harness and appending the measured journal to this document.

## Proposed disposition

For the named artifacts, Codex is a suitable client for the LCTK project endpoint, and the integration is buildable without weakening route-bound scope or putting secrets in committed files. The proposed contract is:

1. LCTK targets Streamable HTTP with `url` plus `bearer_token_env_var`, and does not use inline credentials.
2. LCTK owns the generated `mcp_servers` entry and treats Codex configuration as a generated artifact, never a source of truth for project identity.
3. LCTK never trusts a project identifier supplied by the client or by a repository-local Codex file. The authoritative project identity remains the route, per [ADR-0001](../adr/0001-route-bound-project-scope.md).
4. LCTK provides a local diagnostic path built on `codex doctor --json` semantics rather than inventing its own reachability story.
5. The credential-delivery mechanism, specifically how a project token reaches the editor's environment, is the main remaining design problem and needs its own decision.

This disposition should not be recorded as an accepted ADR until the harness has run and the unverified gates are measured.

## Follow-up

- Install a Go toolchain and run the harness; append the wire-level journal to this document.
- Decide how a project grant token reaches the editor environment, given that extension settings cannot inject it.
- Decide whether LCTK generates a user-global entry, a trusted project-local file, or both, in light of the project-local override finding.
- Decide whether a local OAuth path is worth avoiding environment variables.
- Re-measure when Codex changes materially; this contract is bound to `codex-cli 0.146.0-alpha.9.2`.
