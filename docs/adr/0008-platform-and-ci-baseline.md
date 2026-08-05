# ADR-0008: Platform targets and hosted CI baseline

> Amended by [ADR-0023](0023-managed-podman-wsl-runtime-and-windows-installer.md) for the Windows managed Podman/WSL target and clean-host setup acceptance.

- Status: accepted
- Date: 2026-07-25
- Deciders: project maintainers

## Context

LCTK needs explicit first-platform contracts, while hosted CI does not provide the exact minimum operating systems or Docker Desktop environments. Cross-compilation and hosted tests must not be presented as target certification.

## Decision

The initial compatibility targets are:

- Windows 10 22H2 x86-64 (`windows/amd64`);
- macOS 13 arm64 (`darwin/arm64`);
- Docker Desktop where container behavior is involved.

The Slice 0.1 hosted baseline is `windows-latest` and `macos-15`. Hosted CI proves only behavior exercised on those runner environments. Cross-compilation proves artifact construction, not execution on the target.

Hosted CI does not certify the minimum operating-system versions, Docker Desktop file sharing and lifecycle behavior, filesystem semantics, service installation, archive smoke behavior, signing, or notarization. Certification requires execution on matching physical or trusted self-hosted systems, including extracted-archive smoke tests and Docker Desktop end-to-end tests.

Other platforms are unsupported and best effort until accepted through a later ADR.

## Alternatives considered

- **Treat hosted runner labels as the support contract.** Easy to automate, but runner images change and do not match the minimum target systems.
- **Claim support from cross-compilation.** Produces artifacts without proving runtime behavior.

## Consequences

### Positive

- Platform claims remain evidence-based.
- CI can advance independently from the minimum compatibility contract.
- Remaining certification gaps are visible and testable.

### Negative

- Additional target machines or self-hosted runners are required before a certified release.
- Docker Desktop behavior cannot be validated by the current hosted matrix.

### Follow-up

- Add extracted-archive smoke tests to the artifact workflow.
- Establish trusted target-system test environments before the first supported release.
- Record Docker Desktop, filesystem, installer, and performance evidence in the compatibility matrix.
