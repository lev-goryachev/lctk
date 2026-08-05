# Releasing

## Development artifacts

`Dry-run release artifacts` is a manually triggered, unsigned workflow. It builds the stable launcher and versioned host core for Windows amd64 and macOS arm64, packages the legal notices, extracts each archive, executes `lctk version --json`, generates checksums, and uploads workflow artifacts. Separate native Ubuntu amd64 and arm64 jobs run the production image test target, build the runtime image, verify its platform identity, start it with a read-only workspace, and require container health. Its `-dev` version and lack of platform signatures make it unsuitable for an official release.

[CI run 31012552276](https://github.com/lev-goryachev/lctk/actions/runs/31012552276) and [artifact run 31012555479](https://github.com/lev-goryachev/lctk/actions/runs/31012555479) executed the final matrices at commit `7679060` on 2026-08-05. Windows, macOS, and Linux tests passed; both extracted host archives reported the intended OS and architecture; both native image jobs reported `healthy`; and the amd64 image test included the race detector. The artifact run preserves separate machine-readable host identity and image inspect/health JSON evidence.

The downloaded `0.1.0-stage7-final3` archives independently matched both entries in the published `SHA256SUMS`. The Windows archive was then extracted again on Windows 10 build 19045 with an isolated installation home. Its packaged launcher executed its digest-bound sibling core and reported commit `7679060`, OS `windows`, and architecture `amd64`. This is target-machine dry-run evidence, not Authenticode evidence; the development archive is intentionally unsigned.

## Official release boundary

An official release is created only by pushing a clean `vMAJOR.MINOR.PATCH` tag. The protected `Official release` workflow:

1. derives the Ed25519 public trust root from the protected private key and embeds the public key, key id, and release-manifest URL in both host cores;
2. runs the complete host test suite, then builds the stable launcher, versioned host core, and browser-based setup executable for Windows amd64 plus the existing macOS payload;
3. Authenticode-signs and verifies all three Windows executables;
4. Developer ID-signs both macOS executables, builds a signed installer package, submits it to Apple notarization, and staples and validates its ticket;
5. extracts each archive and executes the packaged launcher on its hosted target architecture;
6. tests, builds, and executes each code-intel architecture on its matching native Ubuntu runner, combines the two immutable manifests, verifies the index platforms, and keyless-signs that index with Sigstore;
7. mirrors the pinned Podman `5.8.2` Windows client and WSL machine image only after exact upstream size and SHA-256 verification;
8. generates SPDX JSON SBOMs, module attribution, checksums, migration and rollback notes, and a schema-2 signed component manifest binding the setup, launcher, core, runtime, images, and model;
9. creates GitHub artifact attestations for the publication set; and
10. publishes the GitHub Release only after every preceding job succeeds.

Publication fails closed when any Ed25519, Authenticode, Developer ID, notarization, GHCR, signature, package-execution, SBOM, checksum, or attestation gate is unavailable or invalid.

The two image architectures are built on `ubuntu-24.04` and `ubuntu-24.04-arm`, not through QEMU. Local measurement found that emulated arm64 cgo compilation did not complete, while a glibc cross-build produced a static-linker NSS warning incompatible with an Alpine runtime claim. Both alternatives were rejected. Native jobs preserve the existing static musl/Alpine boundary and execute the actual architecture before its digest can enter the combined index.

## Protected configuration

The release environment must provide:

- `LCTK_RELEASE_ED25519_PRIVATE_KEY`: base64 Ed25519 seed or private key;
- `LCTK_WINDOWS_PFX_BASE64` and `LCTK_WINDOWS_PFX_PASSWORD`;
- `LCTK_APPLE_CERTIFICATE_BASE64`, `LCTK_APPLE_CERTIFICATE_PASSWORD`, `LCTK_APPLE_APPLICATION_IDENTITY`, and `LCTK_APPLE_INSTALLER_IDENTITY`;
- `LCTK_APPLE_ID`, `LCTK_APPLE_TEAM_ID`, and `LCTK_APPLE_APP_PASSWORD`.

No signing credential, private key, temporary certificate, or keychain is committed or uploaded. Each protected value is scoped only to the shell step that consumes it, so checkout, SBOM, artifact, attestation, and publication actions never receive platform or manifest signing secrets. Temporary certificate and keychain files remain under the checked-out job workspace and are removed before artifact upload.

## Installation and update contract

An extracted package starts through the stable `lctk` launcher. Before initial activation it may execute only its sibling packaged `lctk-core`; a successful `lctk bootstrap --yes` copies that exact core into the versioned installation store and writes the first activation document.

Official setup, bootstrap, and update resolve the signed `release-manifest.json`, validate the embedded trust root, immutable image digests, artifact sizes and SHA-256 digests, model identity, host minimum, and project-schema range. Setup additionally requires the complete Windows launcher, installer, Podman client, and WSL machine set. CLI bootstrap and update remain read-only unless `--yes` is present:

```text
lctk bootstrap --plan
lctk bootstrap --yes
lctk update --plan
lctk update --yes
lctk update rollback
```

Update installs and health-checks the candidate code image for every previously running project before atomically activating the matching host core. Schema 1 to 2 migration runs on a copied SQLite database; the original remains as `semantic.db.rollback-v1`. Any candidate health failure restores migrated projects in reverse order. Explicit rollback verifies the previous core digest, restores available project database bundles, restarts the prior image, and changes the activation document only after those gates pass.

Version rules are defined in [versioning.md](versioning.md), the transaction contract in [ADR-0022](adr/0022-transactional-bootstrap-update-and-release-evidence.md), and the exact evidence boundary in [compatibility.md](compatibility.md).
