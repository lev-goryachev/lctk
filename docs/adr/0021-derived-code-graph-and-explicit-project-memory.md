# ADR-0021: A derived name-matched code graph and explicit reviewed project memory

- Status: accepted
- Date: 2026-08-04
- Deciders: project maintainers

## Context

Stage 6 adds dependency paths, callers, callees, impact analysis, a repository map, and durable project memory. Stage 4 deliberately provides syntax and name matching rather than type resolution. The graph must not imply stronger precision, and memory written by an agent must not become unreviewed source truth.

## Decision

Persist graph facts in the project's semantic SQLite database behind an LCTK-owned graph adapter. Declaration nodes come from Tree-sitter extents. File dependency edges come from language import syntax. Call sites retain the called identifier and enclosing declaration; caller and callee tools resolve them by name and report `precision: name_match`, ambiguity, evidence locations, bounds, and truncation.

Graph state is derived from files on disk and is rebuilt or incrementally replaced with the same generation and freshness contract as semantic chunks. It is not user-authored state. `callers_find`, `callees_find`, `dependency_path`, and `impact_analyze` expose bounded graph actions without exposing SQL or internal node identifiers.

`repository_map` ranks files and declarations by dependency and reference signals, then emits a deterministic character-bounded map with project-relative paths and stated precision. It never drops the truncation flag when the requested budget cannot hold the whole repository.

Project memory is explicit state, separate from the derived graph. Records have a stable key, kind (`decision`, `convention`, `fact`, or `note`), content, confidence, provenance paths, source commit, revision, created and updated timestamps, and optional review time. Writes use optimistic revision checks. The MCP surface is `memory_get`, `memory_search`, `memory_put`, and `memory_delete`; listing is an empty-query search rather than a fifth operation.

Memory search is lexical and semantic when inference is ready, and states which modes contributed. A record is never silently rewritten from repository content or model output. Deletion is explicit. Low-confidence or overdue records remain visible and are labelled rather than hidden.

## Alternatives considered

- **A type-resolved graph presented without project toolchains.** Rejected by ADR-0019 and by the user's explicit decision not to add LSP now.
- **A separate graph database.** Rejected until scale measurements show SQLite traversal is insufficient; another service would add lifecycle and isolation cost before evidence.
- **Automatically generated memory.** Rejected because inference is not authority and stale model-written decisions are more dangerous than missing notes.
- **Opaque vector-only memory.** Rejected because records require exact keys, revisions, provenance, and review metadata.

## Consequences

### Positive

- Graph claims have the same honest precision as the symbol layer that produces them.
- Derived state and authored memory cannot be confused.
- One project database gives transactional updates, backup, migration, and purge semantics.

### Negative

- Name collisions create ambiguous graph edges and larger impact sets.
- SQLite traversal may require a later adapter at extreme scale.
- Useful memory still requires an explicit client write and eventual review.

### Follow-up

- Measure ambiguity and usefulness against real multi-language repositories.
- Include graph and memory schemas in update preflight, backup, and rollback verification.

