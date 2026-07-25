# Versioning

LCTK follows Semantic Versioning for official product releases. One product version identifies the tested set of the `lctk` executable, official images, packaged schemas and templates, migrations, archives, and release metadata.

Dependency versions remain independent and are pinned through the relevant package or image metadata. Public MCP and persistent schema versions are explicit contracts; a product release records which schema versions it contains.

## Before 1.0

Pre-1.0 releases may contain breaking changes. Every breaking public API, configuration, persistent-state, or operational change requires:

- an explicit changelog entry;
- migration and rollback notes where state is affected;
- updated compatibility documentation;
- an ADR when the durable architecture changes.

## Development versions

`0.1.0-dev`, commit builds, and artifacts created by the dry-run workflow are development outputs. They are not releases, establish no supported line, and must not be published as production binaries or images.

See [ADR-0007](adr/0007-unified-versioning.md).
