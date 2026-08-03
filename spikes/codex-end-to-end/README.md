# Codex end-to-end harness

Runs the Slice 1.4 scenario against real components and prints an evidence
report, and since [Slice 3.5](../../docs/roadmap.md#slice-35-stage-3-against-a-second-client)
the Stage 3 tools as well. The measured outcome is recorded in
[`docs/spikes/codex-end-to-end-results.md`](../../docs/spikes/codex-end-to-end-results.md).

It grows with the client-facing surface rather than being frozen: a tool nobody
has called through a real client is a tool whose schema nobody has agreed to.

```bash
go run ./spikes/codex-end-to-end verify
```

Add `--json` for the machine-readable report, `--keep` to leave the work
directory, the containers, and the registrations in place for inspection.

## What is real

- the `lctk` executable, built from the working tree and driven through the same
  commands an operator runs;
- the project endpoint, served by a real `lctk daemon` subprocess on a free port;
- the container runtime, with a real image, network, volume, and read-only source
  mount per project;
- the Codex binary bundled with the installed VS Code extension, which is the
  binary the extension itself runs;
- the configuration LCTK generates, written by `lctk codex config --apply`;
- the credential, delivered in the environment of a process the harness starts,
  which is the mechanism [ADR-0014](../../docs/adr/0014-project-credential-delivery.md)
  specifies;
- the Git repository the Stage 3 tools describe, created and committed by the
  harness, with an uncommitted edit for `git_status` and `git_diff` to report;
- the container the approved command runs in, using the image this repository
  builds so nothing external is assumed.

Nothing is simulated. A step whose component is unavailable is reported as
skipped, never as passed. Without a Linux-capable container runtime the run
skips everything rather than reporting a hollow pass.

## What is not covered

The harness drives the Codex binary directly, so it does not click the
extension's user interface. That gap is stated in the results document together
with the manual steps that close it.

The app-server methods used here are experimental. Per
[ADR-0012](../../docs/adr/0012-codex-integration-contract.md) they are a
verification driver only, and no LCTK production code depends on them.

## Isolation

Every run works inside a temporary directory with its own `LCTK_HOME` and its own
`CODEX_HOME`. The operator's real registry, grants, and Codex configuration are
neither read nor modified. Projects the run registers are removed at the end
unless `--keep` is given.

## Relationship to the Slice 0.4 harness

`appserver.go` is a copy of the driver in
[`spikes/codex-compatibility/`](../codex-compatibility/). That harness is the
frozen artifact behind ADR-0012 and is named in the Slice 0.4 results, so it is
left byte-stable rather than refactored into a shared package.
