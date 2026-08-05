# ADR-0022: Transactional bootstrap, update rollback, and evidence-gated releases

- Status: accepted
- Date: 2026-08-04
- Deciders: project maintainers
- Amended by: [ADR-0023](0023-managed-podman-wsl-runtime-and-windows-installer.md) for runtime components and the Windows setup transaction

## Context

Stage 7 must turn development components into an installable and updatable product without making an interrupted migration or a mutable download authoritative. Windows amd64 and macOS arm64 remain development compatibility targets. As amended by ADR-0023, the first official publication target is Windows amd64 only, and protected release credentials must never enter the repository.

## Decision

`lctk bootstrap` first produces a complete plan: prerequisites, immutable component identities, download sizes, disk estimate, and compatibility. Without `--yes`, it asks once before any download. Applying the plan verifies every digest, installs the matching images and embedding model, writes no project source, and runs a functional self-test.

`lctk update` resolves one signed release manifest, verifies product and schema compatibility, inventories every project, checks free space, and creates a rollback bundle before changing state. Migrations run on copies, validate the result, and are swapped atomically. Component activation occurs only after host, image, model, and schema checks pass. A failed activation restores the previous executable selection, images, model, and project databases. `--plan` performs every read-only preflight and writes nothing.

Official releases are produced only from a version tag and clean protected workflow. The initial release set includes the Windows amd64 setup, launcher, core, and archive; versioned multi-architecture images; the pinned runtime and model artifacts; checksums; generated dependency notices; SPDX SBOMs; SLSA-compatible provenance; signatures; release notes; migration notes; and rollback instructions. macOS artifacts are excluded from the official inventory under ADR-0023.

GitHub artifact attestations and keyless container signatures provide repository provenance. As amended by ADR-0023, Windows executables are intentionally unsigned and depend on the tagged workflow, launcher binding, checksums, attestations, and Ed25519 component manifest for release identity. The initial official workflow has no platform-certificate dependency. Development dry runs, including macOS archives, remain visibly non-release artifacts.

Compatibility and stress evidence is generated separately from publication. Development binaries are extracted and executed on both hosted compatibility architectures, while the official workflow packages Windows only. Managed Podman integration, restart persistence, MCP client paths, and the parameterized stress suite must pass for a Windows release candidate. Hosted evidence is not described as certification of Windows 10, macOS 13, WSL2 virtualization, or Docker Desktop hardware that was not actually tested.

## Alternatives considered

- **Replace files in place without a rollback bundle.** Rejected because an interrupted schema or binary update would strand persistent projects.
- **Trust HTTPS without immutable digests and provenance.** Rejected because a mutable upstream response would become installed code.
- **Publish ordinary development artifacts as a release.** Rejected because they omit the version identity, embedded trust root, complete component manifest, attestations, and evidence required by the tagged workflow.
- **Silently skip a declared release gate when credentials are absent.** Rejected; every declared publication gate must fail closed. Windows Authenticode is deliberately not a declared gate under ADR-0023.

## Consequences

### Positive

- Bootstrap and update have a reviewable plan and one activation boundary.
- Persistent state is never migrated without a verified rollback copy.
- Every published component maps to one product version and verifiable evidence.

### Negative

- Official publication requires the protected manifest key and protected CI environment, but no paid platform certificate.
- Unsigned Windows executables can trigger warnings or be blocked by managed-device policy.
- Release validation is slower and more expensive than ordinary CI.
- Rollback bundles temporarily require additional disk proportional to mutable state.

### Follow-up

- Keep external pull requests closed until the contribution policy is deliberately opened; completing the old Slice 1.5 implementation is not by itself that policy decision.
- Record target-hardware certification outside public source where private device records are involved.
