# Stage 7 MCP client-path verification

## Scope

The acceptance fixture was a five-file project registered as `fixture-2bwi74u7`, served by the real Windows host daemon, a real Docker code-intel container, the shared pinned inference container, and a persistent project volume. The service published exact, semantic, and graph generation 3 with five files, 13 graph nodes, three imports, and nine calls.

## Independent protocol client

A separate Go program using `github.com/modelcontextprotocol/go-sdk` v1.7.0 connected over Streamable HTTP with the project bearer grant. It listed exactly 18 tools and invoked the complete catalog. All ordinary read and memory lifecycle operations succeeded. `run_command` with an unapproved name and a stale memory revision produced their expected typed refusals. A separate put, process restart, get, and delete sequence recovered the same memory content and revision after the project service moved to a new loopback port.

After the Stage 7 bounded semantic ranking, concurrent exact inventory, exact-only startup correction, and crash-complete schema migration recovery, the same independent client was run again against the rebuilt final local image `sha256:4785290b20b5d707d8515e13c5bb800da8461cfd83802b49dc7469da80743d9e` from commit `7679060`. The container identity matched the selected tag, all 18 tools were listed and called again, and both expected refusals remained typed. The one-time fixture grant was revoked immediately after this final pass and its revoked state was verified separately.

## Codex agent client

Codex CLI `0.146.0-alpha.9.2` ran as an actual agent against an LCTK-generated entry in an isolated repository-local `CODEX_HOME`. The grant was supplied only through the generated environment-variable name. The first automation attempt demonstrated that ordinary non-interactive approval mode cancels MCP calls before dispatch; the accepted run used Codex's explicit automation bypass in a disposable fixture and restricted the agent instruction to MCP tools only.

The agent called every catalog entry and observed:

| Tool | Result |
|---|---|
| `project_info` | PASS: healthy fixture and route scope |
| `exact_search` | PASS: two `RetryFailedRequest` matches |
| `git_status` | PASS: clean fixture repository |
| `git_diff` | PASS: empty patch |
| `run_command` | EXPECTED REFUSAL: `COMMAND_UNKNOWN` |
| `file_outline` | PASS: valid Go declaration |
| `find_definition` | PASS: `retry.go:6` |
| `find_references` | PASS: declaration and JavaScript call |
| `code_search_semantic` | PASS: `retry.go` ranked first |
| `callers_find` | PASS: JavaScript caller `start` |
| `callees_find` | PASS: bounded callees |
| `dependency_path` | PASS: `main.js` to `dep.js` |
| `impact_analyze` | PASS: direct impact evidence |
| `repository_map` | PASS: five files, 13 nodes, untruncated |
| `memory_get` | EXPECTED REFUSAL before creation, PASS after creation |
| `memory_search` | PASS: semantic and lexical evidence |
| `memory_put` | PASS: revision 1 with provenance |
| `memory_delete` | PASS: revision-checked deletion |

Where a tool accepted a `project_id`, the agent deliberately supplied a foreign value. Every response remained bound to `fixture-2bwi74u7` with `scope_source: route_and_registry`. No host path or foreign-project data appeared. The temporary Codex authorization was removed with `codex logout` and its absence was verified after the run.

## Boundary of the claim

This proves the complete current catalog through Codex and an independent MCP SDK client on one Windows 10 and Docker Desktop installation. It does not certify another Codex version, the VS Code extension UI, macOS, concurrent agent sessions, or OAuth-only MCP clients.
