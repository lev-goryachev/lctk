# ADR-0004: Stable aggregated MCP tool API

- Status: accepted
- Date: 2026-07-24
- Deciders: project maintainers

## Context

Exact, semantic, LSP, AST, and graph engines will be replaced over time. If the client sees backend-specific tools, changing an engine breaks prompts, clients, and compatibility.

## Decision

LCTK exposes project-level tools named for user actions and hides backend topology behind an aggregating `code-intel` boundary.

Responses normalize paths, deduplicate results, limit their size, and return provenance, freshness, and index generation. Public schemas are versioned independently of adapter implementations.

## Alternatives considered

- **Connect every backend MCP directly.** Faster initially, but creates an enormous tool catalog and couples clients to vendors.
- **Rename backend tools in the gateway.** Insufficient: result schemas, ranking, and consistency still differ.
- **One universal `query` tool.** Compact, but loses typed schemas and impairs tool selection.

## Consequences

### Positive

- A backend can be replaced without client migration.
- Capability profiles expose a small, relevant catalog.
- Results become token-efficient and comparable.

### Negative

- LCTK must maintain the adapter contract, result fusion, and schema versioning.
- Not every backend-specific capability is immediately exposed.

### Follow-up

- Define the v1 schemas for `project_info` and the lexical-search tool `exact_search` in the first product slice.
- Define the compatibility policy and deprecation window.
