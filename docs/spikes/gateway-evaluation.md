# Gateway evaluation contract

## Status

Accepted Slice 0.2 test contract. Candidate selection remains open until the measured results are reviewed with the maintainer.

Evaluation date: 2026-07-25.

## Candidates

The same externally observable scenario is evaluated against pinned revisions of:

- IBM ContextForge;
- Docker MCP Gateway;
- MCPJungle;
- a minimal LCTK-owned Go gateway using the official MCP Go SDK.

A candidate's built-in administration, catalog, runner, or Docker lifecycle features do not receive credit unless they satisfy an LCTK gateway responsibility. The coding gateway must not receive Docker API access.

## Required scenario

The harness creates two independent upstream Streamable HTTP MCP servers:

- project `alpha`, exposing `project_info` and an `alpha_only` sentinel;
- project `beta`, exposing `project_info` and a `beta_only` sentinel.

Each `project_info` response is generated from immutable server-side context. Its input schema contains no authoritative project selector.

The candidate must expose or be adaptable behind:

```text
/projects/alpha/mcp
/projects/beta/mcp
```

The harness verifies:

1. both routes initialize through the official MCP Go client;
2. each route lists only its assigned tools;
3. each route returns its server-assigned project identity;
4. a model-supplied `project_id`, root, or absolute path cannot change scope;
5. an alpha-only grant cannot access beta, and vice versa;
6. missing, invalid, and mismatched grants produce stable typed failures;
7. a project can be registered, changed, and removed without restarting the shared gateway;
8. a reconnect preserves stable project identity without treating the MCP session ID as the project ID;
9. an upstream failure produces a bounded, attributable error rather than empty success;
10. the gateway container runs without the Docker socket or another project mount.

If a candidate cannot implement a requirement natively, the evaluation records the exact adapter that would be required. An adapter that becomes the authoritative router, grant validator, and error translator is counted as a custom gateway rather than attributing those capabilities to the external product.

## Typed failure contract

The spike uses this provisional envelope at the HTTP/control boundary:

```json
{
  "code": "AUTH_FORBIDDEN",
  "message": "The client grant does not permit this project.",
  "retryable": false,
  "project_id": "beta",
  "request_id": "..."
}
```

Minimum codes under test:

- `PROJECT_NOT_FOUND`;
- `AUTH_REQUIRED`;
- `AUTH_FORBIDDEN`;
- `SERVICE_UNAVAILABLE`;
- `INTERNAL_ERROR`.

This envelope is a spike contract, not yet the public v1 schema.

## Measurements

Measurements are repeated after one warm-up and reported with the environment:

- cold process/container readiness time;
- idle resident memory and CPU;
- image and required persistent-state size;
- `initialize`, `tools/list`, and `tools/call` success rate;
- warm `tools/call` latency over at least 100 sequential calls;
- dynamic registration and removal latency;
- shutdown behavior and leaked processes/containers;
- configuration volume and LCTK-owned adapter code required.

Absolute timings are evidence for this machine only. They are not target-platform guarantees.

## Scoring

Hard gates are evaluated before weighted scoring.

### Hard gates

A production candidate must:

- support current Streamable HTTP behavior used by the official Go SDK;
- enforce route-bound project scope before tool dispatch;
- validate per-client project grants;
- support dynamic project registration without full gateway restart;
- run without Docker socket access;
- use a license compatible with Apache-2.0 distribution;
- expose enough health and failure information for typed LCTK diagnostics.

A failed hard gate is acceptable only when a small, clearly bounded LCTK adapter can supply it without becoming the gateway itself.

### Weighted criteria

| Criterion | Weight |
|---|---:|
| Project-scope and authorization correctness | 30 |
| MCP protocol behavior and reconnect reliability | 20 |
| Dynamic lifecycle and configuration model | 15 |
| Operational simplicity and resource overhead | 15 |
| Stable API fit and adapter size | 10 |
| Maintenance, release, and license posture | 10 |

Scores use a five-point scale and must cite either a reproducible test or an exact upstream source/documentation reference.

## Decision rule

The recommendation chooses the simplest candidate that passes the hard gates and preserves ADR-0001, ADR-0002, and ADR-0004. Feature breadth outside LCTK's gateway boundary is not a reason to accept excess privilege, ambiguous project scope, or a second administrative control plane.

The evaluation results and recommendation are presented to the maintainer before an ADR is accepted or production code adopts a gateway implementation.
