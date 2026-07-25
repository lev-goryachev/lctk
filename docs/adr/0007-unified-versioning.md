# ADR-0007: Unified product versioning

- Status: accepted
- Date: 2026-07-25
- Deciders: project maintainers

## Context

LCTK will distribute a host executable, official images, schemas, templates, migrations, and release metadata. Independent release trains would make compatibility and rollback difficult before operational evidence shows they are necessary.

## Decision

Use one product Semantic Version for each official LCTK release train.

The official executable, images, packaged schemas and templates, migrations, archives, and release metadata carry or map to that product version. Dependency module versions remain independent.

Public MCP and persistent schema versions remain explicit and may evolve independently from adapter implementations as required by ADR-0004, but each product release documents the schema versions it contains and their compatibility. Before 1.0, breaking changes require explicit changelog and migration notes.

Do not create independently released official LCTK components until measured operational requirements justify separate release trains. Development identifiers such as `0.1.0-dev` and dry-run archives are not releases and create no support line.

## Alternatives considered

- **Independent component versions from the start.** Flexible, but creates an unnecessary compatibility matrix before components have independent operational lifecycles.
- **Commit hashes only.** Precise for developers, but unsuitable for user-facing upgrades, image selection, and migrations.

## Consequences

### Positive

- Users receive one compatibility and rollback reference.
- Archive, image, schema, and migration selection is deterministic.
- Release documentation can describe one tested component set.

### Negative

- A change in one official component may advance the product version for all artifacts.
- Schema compatibility still needs explicit metadata; a product version does not replace schema versions.

### Follow-up

- Define the first release tag, image-tag, compatibility-table, migration, rollback, and deprecation contracts.
- Add release validation that checks embedded versions and artifact names.
