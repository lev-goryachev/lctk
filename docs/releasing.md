# Releasing

## Current Slice 0.1 process

The `Dry-run release artifacts` workflow is manually triggered with a development version. It:

1. builds a Windows amd64 `lctk.exe` and a Darwin arm64 `lctk` binary;
2. embeds the requested version, commit, and build timestamp;
3. packages `LICENSE`, `NOTICE`, `README.md`, and `THIRD_PARTY_NOTICES.md`;
4. creates `lctk-<version>-windows-x86_64.zip` and `lctk-<version>-darwin-arm64.tar.gz`;
5. generates `SHA256SUMS`;
6. uploads workflow artifacts only.

The workflow does not create a Git tag or GitHub Release, publish container images, extract and execute the archives, sign or notarize artifacts, generate an SBOM or provenance statement, or establish production support.

## First-release gates

Before publishing an official release:

- verify a clean checkout and the full CI baseline;
- execute packaged binaries on the target platforms;
- complete dependency-license attribution;
- define tag and image conventions;
- document migrations, rollback, and compatibility;
- generate checksums, SBOM, and provenance;
- sign Windows artifacts and sign/notarize macOS artifacts;
- verify Docker Desktop behavior required by that release;
- publish release notes and update the changelog.

Version rules are defined in [versioning.md](versioning.md) and [ADR-0007](adr/0007-unified-versioning.md). Platform evidence is defined in [compatibility.md](compatibility.md) and [ADR-0008](adr/0008-platform-and-ci-baseline.md).
