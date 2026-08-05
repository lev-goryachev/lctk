# ADR-0022: Transactional bootstrap, update rollback, and evidence-gated releases

- Status: accepted
- Date: 2026-08-04
- Deciders: project maintainers

## Context

Stage 7 must turn development components into an installable and updatable product without making an interrupted migration or a mutable download authoritative. The first targets are Windows amd64 and macOS arm64, and official signing and notarization depend on protected release credentials that must never enter the repository.

## Decision

`lctk bootstrap` first produces a complete plan: prerequisites, immutable component identities, download sizes, disk estimate, and compatibility. Without `--yes`, it asks once before any download. Applying the plan verifies every digest, installs the matching images and embedding model, writes no project source, and runs a functional self-test.

`lctk update` resolves one signed release manifest, verifies product and schema compatibility, inventories every project, checks free space, and creates a rollback bundle before changing state. Migrations run on copies, validate the result, and are swapped atomically. Component activation occurs only after host, image, model, and schema checks pass. A failed activation restores the previous executable selection, images, model, and project databases. `--plan` performs every read-only preflight and writes nothing.

Official releases are produced only from a version tag and clean protected workflow. The release set includes Windows amd64 and macOS arm64 archives, versioned multi-architecture images, the pinned model manifest, checksums, generated dependency notices, SPDX SBOMs, SLSA-compatible provenance, signatures, release notes, migration notes, and rollback instructions.

GitHub artifact attestations and keyless container signatures provide repository provenance. Windows Authenticode and Apple signing/notarization are mandatory release jobs backed by protected secrets; a workflow without those credentials fails closed and cannot publish an official release. Development dry runs remain unsigned and visibly non-release artifacts.

Compatibility and stress evidence is generated separately from publication. Packaged binaries are extracted and executed on both hosted target architectures. Docker integration, restart persistence, MCP client paths, and the parameterized stress suite must pass for a release candidate. Hosted evidence is not described as certification of Windows 10, macOS 13, or Docker Desktop hardware that was not actually tested.

## Alternatives considered

- **Replace files in place without a rollback bundle.** Rejected because an interrupted schema or binary update would strand persistent projects.
- **Trust HTTPS without immutable digests and provenance.** Rejected because a mutable upstream response would become installed code.
- **Publish unsigned development artifacts as a release.** Rejected because it removes the distinction the current versioning contract depends on.
- **Silently skip platform signing when credentials are absent.** Rejected; publication must fail closed.

## Consequences

### Positive

- Bootstrap and update have a reviewable plan and one activation boundary.
- Persistent state is never migrated without a verified rollback copy.
- Every published component maps to one product version and verifiable evidence.

### Negative

- Official publication requires platform credentials and protected CI environments.
- Release validation is slower and more expensive than ordinary CI.
- Rollback bundles temporarily require additional disk proportional to mutable state.

### Follow-up

- Keep external pull requests closed until the contribution policy is deliberately opened; completing the old Slice 1.5 implementation is not by itself that policy decision.
- Record exact signing identities and target-hardware certification outside public source where credentials or private device records are involved.

