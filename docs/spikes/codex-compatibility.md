# Codex compatibility verification contract

## Status

Accepted Slice 0.4 verification contract. The resulting integration contract is recorded in [ADR-0012](../adr/0012-codex-integration-contract.md), supported by the [measured results](codex-compatibility-results.md).

Contract date: 2026-07-31.

## Purpose

Slice 0.4 must produce a verified Codex integration contract rather than an assumption. Every claim in the results document must name the artifact it was measured against and the method used, or be marked unverified.

This is not a gateway or backend selection. There is one client under test. The question is what the actual Codex client requires from an LCTK project endpoint, and what LCTK must generate on the host to make a project connectable.

## Artifacts under test

The contract binds to exact artifacts, because Codex ships fast and pre-1.0 behavior changes:

- VS Code extension `openai.chatgpt`, version `26.727.40816-win32-x64`, displayed as "Codex – OpenAI's coding agent";
- the CLI bundled inside that extension, `codex-cli 0.146.0-alpha.9.2`;
- the current official Codex MCP documentation at the time of measurement.

The bundled CLI is authoritative for this spike because it is the binary the extension actually runs. A separately installed `codex` on `PATH` is not a substitute and must be recorded separately if used.

Third-party guides are not evidence. Where a widely circulated guide disagrees with a measurement, the results document records the disagreement and the measurement wins.

## Evidence rules

1. Configuration behavior is established by running the CLI, not by reading documentation alone. Documentation is cited to confirm intent and defaults.
2. Measurements never read or modify the operator's real Codex state. Every run points `CODEX_HOME` at an isolated directory under an ignored repository-local research path.
3. No measurement may start a model turn. The spike must not consume account quota, and a verification path that requires a paid model call is rejected as a harness design.
4. Secrets are never recorded. Token values, account identifiers, and machine identifiers are excluded from committed evidence; only the presence, scheme, and origin of a credential is recorded.
5. A behavior that was not exercised is reported as unverified. Absence of an error is not evidence of support.

## Questions that must be answered

These correspond to the roadmap Slice 0.4 items and to the open Codex questions.

### Q1. Configuration path and schema

- Which files does Codex read to discover MCP servers, in what precedence order?
- Is a project-local file supported, and under what preconditions?
- What is the exact accepted schema for a Streamable HTTP server?
- Which fields are rejected, and how does Codex report a rejection?
- What happens to unrelated servers when one entry fails to parse?

### Q2. Streamable HTTP fields

- Is Streamable HTTP a first-class transport, or an experimental one?
- Which timeout, enable, and tool-filtering fields exist, and what are their defaults?
- Can static and environment-derived HTTP headers be attached to requests?

### Q3. Bearer token supply

- Can a bearer token be placed in configuration, or must it come from the environment?
- Which environment variable is read, and by which process?
- Is a key helper, credential command, or local proxy available as an alternative?
- What does Codex do when the referenced variable is unset?

### Q4. Required server behavior

- What does the real client send to a project endpoint: HTTP method, `Accept`, protocol-version header, session handling, and `Authorization`?
- Does the client tolerate a stateless JSON response, or does it require session and streaming support?
- Does a typed server error reach the client intact?
- Is a route-bound project scope respected, so that a credential issued for one project cannot be used on another project's route?

### Q5. Reload and reconnect UX

- Is there a reload mechanism that does not require restarting the editor?
- What is the observable behavior after configuration changes while a session is open?
- What happens when a configured project endpoint is unreachable or stopped?

### Q6. Generation and trust surface

- Which of the required files and values must LCTK generate, and where do they live?
- Which of them may be committed to a repository, and which must remain local?
- What can a repository author change about MCP configuration in a trusted project?

## Method

### Direct CLI measurement

Each configuration question is answered by writing a candidate `config.toml` into an isolated `CODEX_HOME` and observing:

- `codex mcp get <name> --json` and `codex mcp list --json` for the accepted, normalized schema;
- the exact loader error text for a rejected field;
- `codex doctor --json`, whose `config.load` check reports the resolved `CODEX_HOME`, `config.toml`, `cwd`, and loaded server count, and whose `mcp.config` check performs real endpoint reachability probing and environment-variable validation with no credentials and no model call.

Discovery and precedence are measured with a dedicated fixture: an independent Git repository containing a repository-root `.codex/config.toml` and a nested `.codex/config.toml`, exercised from several working directories, with and without a project trust record.

### Tracked harness

A tracked Go harness under [`spikes/codex-compatibility/`](../../spikes/codex-compatibility/) provides the server side and drives the client:

- it serves an LCTK-shaped project endpoint at `/projects/{project_id}/mcp` over Streamable HTTP, with the project identity bound to the route and a per-route bearer token;
- it records each HTTP exchange in a journal capturing method, path, status, recorded headers, JSON-RPC method names, the `Authorization` scheme, and whether the presented token matched the route, never the token value;
- it exposes `project_info`, which returns the route-bound project identity and ignores any model-supplied `project_id`, and `typed_error`, which always fails with a typed LCTK code;
- it generates the `CODEX_HOME` configuration that LCTK would have to produce, with TOML escaping, deterministic key order, and no representable secret field;
- it drives the real client through `codex app-server --listen stdio://` using `initialize`, `mcpServerStatus/list`, and `config/mcpServer/reload`, which exercises the actual Codex MCP client without a model turn.

The harness is evidence. It does not become production code and does not become an LCTK dependency.

Steps that need the Codex CLI are skipped explicitly and reported as skipped when the CLI is unavailable. They are never simulated. The harness lives in the root Go module, so its unit tests run in hosted CI on Windows and macOS, where no Codex CLI is present; those tests must therefore cover only the parts that do not require Codex.

## Hard gates

A gate failure blocks the accepted integration contract for the artifacts under test and must be recorded as a failure rather than worked around.

1. **Streamable HTTP is usable.** The client connects to an LCTK-shaped `/projects/{project_id}/mcp` endpoint and completes an MCP handshake.
2. **Credentials never require a secret in a committed file.** A working configuration exists in which no token value appears in any file that could reasonably be committed.
3. **Route-bound scope survives the client.** A credential issued for one project does not grant access on another project's route, and a model-supplied project identifier does not change the server-enforced scope.
4. **Typed errors survive.** A typed server-side failure is distinguishable by the client from a transport failure.
5. **Reload does not require reinstalling or restarting the editor.** A configuration change can be picked up through a documented mechanism.
6. **Unreachable projects are diagnosable locally.** An operator can determine, without a model turn, whether a configured project endpoint is reachable and whether its credential is present.

## Recorded outcome

The results document must state, for every question above:

- the answer;
- the artifact and command that produced it;
- whether the answer is verified, documented-only, or unverified;
- the consequence for LCTK, especially for [ADR-0001](../adr/0001-route-bound-project-scope.md) route-bound scope and for the Slice 1.5 end-to-end scenario.

Version skew is recorded explicitly. The contract applies to the named artifacts, and a materially different Codex version requires re-measurement before the integration is claimed to work.
