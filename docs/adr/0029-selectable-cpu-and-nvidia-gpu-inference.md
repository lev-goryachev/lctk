# ADR-0029: Selectable CPU and NVIDIA GPU inference distributions

- Status: accepted
- Date: 2026-08-06
- Deciders: project maintainers
- Supersedes: the CPU-only inference distribution in [ADR-0020](0020-shared-embedding-and-project-semantic-store.md)
- Amends: [ADR-0022](0022-transactional-bootstrap-update-and-release-evidence.md), [ADR-0023](0023-managed-podman-wsl-runtime-and-windows-installer.md), [ADR-0024](0024-native-windows-setup-and-user-selected-storage.md), and [ADR-0027](0027-native-setup-in-place-upgrade-and-repair.md)

## Context

The first full semantic index on the supported CPU distribution embedded 1,169 chunks in 30 minutes 36 seconds. LCTK must retain a CPU path for machines without a compatible NVIDIA adapter while allowing the machine owner to select a local NVIDIA GPU distribution when the complete Windows, WSL, CDI, image, and model path is verifiably compatible.

The inference service remains installation-wide stateless compute. Project text, embeddings, indexes, credentials, and OAuth state remain project-scoped or host-owned exactly as defined by ADR-0020, ADR-0023, and ADR-0026. A GPU selection must not introduce cloud inference, Docker Desktop, a second WSL distribution, ambient host package state, published WSL ports, or a silent CPU fallback.

An update or distribution change also cannot replace the shared container destructively before its successor is proven. The inference container may be attached to multiple isolated project networks, each with the fixed `lctk-inference` alias. Losing those attachments would break running projects and violate transactional update behavior.

## Decision

Setup presents exactly two inference distributions: `CPU` and `NVIDIA GPU`. CPU is the default for a new installation. An existing installation keeps its persisted selection unless the owner explicitly changes it in the accepted setup plan.

The selection is stored in a separate owner-only, schema-versioned `inference.json` document under the LCTK installation home. A missing document means CPU. The host settings schema is not changed because the verified `0.1.11` rollback target must continue to read its existing settings. The selection file is committed only after the selected distribution passes installation, activation, and a real embedding self-test; failure restores the preceding document atomically.

The signed release manifest retains the existing CPU `inference_image` identity and adds one required NVIDIA GPU image identity plus the exact NVIDIA CDI package artifact. Both distributions use the same pinned llama.cpp source generation and the same pinned embedding model. Setup verifies signed size and SHA-256 before a package or image crosses the managed-machine boundary. No package repository is added to the managed machine.

The NVIDIA GPU distribution is supported only when every gate succeeds:

1. Windows reports a compatible NVIDIA adapter, driver, VRAM, and compute capability.
2. The managed WSL machine exposes `/dev/dxg` and its projected NVIDIA driver libraries.
3. The exact signed `nvidia-container-toolkit-base` RPM installs and verifies at the pinned NEVRA.
4. `nvidia-ctk cdi list` exposes `nvidia.com/gpu=all` through Podman.
5. The digest-pinned CUDA image starts with `--device nvidia.com/gpu=all` and explicit llama.cpp GPU-layer offload.
6. Diagnostics identify the expected physical GPU, CUDA backend, and offloaded model layers.
7. The real OpenAI-compatible embeddings endpoint returns one finite vector with the pinned 768 dimensions.

Each failed gate returns a typed, actionable error. LCTK never starts the CPU image as a fallback while the persisted or requested distribution is NVIDIA GPU.

Activation uses a candidate-and-swap transaction. LCTK starts the selected image under a temporary managed name on the runtime network, performs health, backend, and embedding checks, and records the current container's complete user-defined network attachments and aliases. Only then does it move the old container to its rollback name, disconnect its project networks to prevent duplicate DNS aliases, move the candidate to the final name, and reconnect the exact recorded project networks with their aliases. Any swap or reconnection failure removes the candidate, reconnects and renames the previous container, proves its health, and leaves the previous selection active. Unexpected inspect output is fatal; it is never treated as absence.

Changing from NVIDIA GPU to CPU changes the selected image, device injection, runtime arguments, and reported backend. It does not uninstall the verified CDI package or delete either cached image. Retaining immutable cached components makes repeat switching offline-capable and avoids mutating the managed machine's base packages during a distribution change. Complete uninstall still removes the installation-owned Podman machine and therefore all CDI, image, model, and selection state.

An ordinary update installs and verifies all signed host artifacts, the selected inference distribution, and any code image before stopping a project or activating the new host version. The update continues the current distribution unless setup explicitly records a change. Rolling back to `0.1.11` makes its older core use the CPU distribution because that core does not read `inference.json`; the selection and verified GPU components remain inert and resume on a later compatible update.

Admin reports requested distribution, actual backend, immutable image identity, readiness, and typed failure. For NVIDIA GPU it also reports adapter name, driver, VRAM, compute capability, CDI device identity, CUDA backend, and offloaded-layer evidence. It must not infer GPU readiness solely from the selected value or container image name.

## Alternatives considered

### Replace the CPU distribution with CUDA everywhere

Rejected. Machines without a compatible NVIDIA adapter remain supported, and a CUDA image cannot provide an honest CPU fallback contract.

### Install the full NVIDIA Container Toolkit from a live repository

Rejected. Podman needs CDI generation, not the legacy Docker runtime hook. The full package adds unused dependencies, and a live repository would make repeatability depend on mutable external state.

### Store the distribution in the existing host settings schema

Rejected. A schema bump would make the verified `0.1.11` rollback target reject the settings document.

### Remove GPU components when CPU is selected

Rejected. Removal adds rollback risk and prevents an offline repeat switch without improving runtime isolation; CPU activation already omits CDI devices and GPU offload arguments.

### Replace the running inference container in place

Rejected. The current container can serve multiple project networks. Destructive replacement before candidate proof can lose network attachments and leaves no verified rollback target.

### Treat a healthy CUDA container as GPU proof

Rejected. A CUDA-capable image can start without actually offloading model layers. Acceptance requires backend diagnostics plus a real embedding result.

## Consequences

### Positive

- CPU remains explicit and fully supported.
- NVIDIA acceleration is observable, reproducible, and cannot silently degrade to CPU.
- Distribution switches and updates preserve running project network topology or restore the previous service.
- Installed immutable components support offline repair and repeat switching.
- `0.1.11` remains a readable rollback target without compatibility shims in its settings schema.

### Negative

- A complete release carries and verifies two inference image identities and one additional RPM artifact.
- The CUDA image requires substantially more download and storage than the CPU image.
- GPU readiness has host, WSL, CDI, container, backend, and model gates, each of which must remain diagnosable.
- A rollback to `0.1.11` temporarily operates CPU inference even when `inference.json` retains NVIDIA GPU for a later compatible version.

### Follow-up

- Implement and test the immutable plan recorded in [the NVIDIA GPU inference dry run](../spikes/nvidia-gpu-inference-installer-dry-run.md).
- Measure a fresh full semantic reindex on the GTX 1070 against the 30-minute-36-second CPU baseline.
- Record installed-image size, real GPU diagnostics, update/rollback evidence, and Admin evidence in `docs/compatibility.md`.
