# Slice 0.4 Codex compatibility harness

Evidence harness for the [Codex compatibility verification contract](../../docs/spikes/codex-compatibility.md). Measurements are recorded in the [results document](../../docs/spikes/codex-compatibility-results.md).

This is a spike. It is not production code, it is not an LCTK dependency, and nothing here defines the public LCTK MCP API.

## What it does

- serves an LCTK-shaped project endpoint at `/projects/{project_id}/mcp` over Streamable HTTP, with the project identity bound to the route and a per-route bearer token;
- journals every HTTP exchange: method, path, status, selected headers, JSON-RPC method names, the `Authorization` scheme, and whether the presented token matched the route. Token values are never recorded;
- exposes `project_info`, which answers from the route and ignores any model-supplied `project_id`, and `typed_error`, which always fails with a typed LCTK code;
- generates the `CODEX_HOME` `config.toml` that LCTK would have to produce, with TOML escaping and deterministic key order, and with no field capable of holding a secret;
- drives the real Codex client through `codex app-server --listen stdio://` using `initialize`, `mcpServerStatus/list`, and `config/mcpServer/reload`, which exercises the actual MCP client without starting a model turn.

## Safety properties

- The operator's real Codex state is never read or modified. Every run points `CODEX_HOME` at an isolated directory under the working directory.
- No model turn is started, so no account quota is consumed and no credentials are required.
- Generated configuration cannot contain a token, because Codex rejects an inline `bearer_token` for a Streamable HTTP server.

## Usage

Run the whole scenario and write a report:

```bash
go run ./spikes/codex-compatibility verify --out .research/codex-compat/report.json
```

The Codex CLI is discovered from `--codex`, then `LCTK_CODEX_BIN`, then `PATH`, then the bundled binary inside an installed `openai.chatgpt-*` VS Code extension. When no CLI is found, the Codex-dependent steps are reported as **skipped**; they are never simulated.

Serve the endpoint alone, for manual inspection with an already-configured client:

```bash
go run ./spikes/codex-compatibility serve --project lctk_alpha --token dev-token
```

Print the configuration LCTK would generate:

```bash
go run ./spikes/codex-compatibility config --project lctk_alpha --base-url http://127.0.0.1:8123
```

## Verification status

The unit tests cover the parts that do not need Codex: configuration generation and escaping, JSON-RPC method extraction, the route token guard, typed error codes, route-bound scope, and the journal's no-secret property. They run in hosted CI on Windows and macOS, where no Codex CLI is present.

The `verify` path was executed against the real Codex CLI on Windows 10 with Go 1.26.5 and `codex-cli 0.146.0-alpha.9.2`. All eight steps passed:

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

The measured journal and the bounds of that claim are in the [results](../../docs/spikes/codex-compatibility-results.md), including [Limits of this evidence](../../docs/spikes/codex-compatibility-results.md#limits-of-this-evidence). One run on one host against one alpha Codex build is not a support claim.
