# Development

## Prerequisites

- Go 1.25.x;
- Git;
- Docker Desktop only for Docker integration work.

Docker Desktop is not required for the current unit tests. `lctk doctor` reports an actionable error when its daemon is unavailable.

## Checks

On Windows PowerShell:

```powershell
./scripts/check.ps1
```

On macOS or another POSIX shell:

```sh
./scripts/check.sh
```

The checks require canonical `gofmt` output, run `go vet ./...`, and execute `go test -cover ./...`.

## Build and run

```sh
go build -trimpath -o lctk ./cmd/lctk
./lctk version
./lctk daemon
```

The foreground daemon listens on `127.0.0.1:4444` by default. Slice 0.1 provides:

- `GET /health`;
- Streamable HTTP MCP at `/mcp` with temporary compatibility tool `foundation_info`;
- `lctk watch-once <directory>` for the basic watcher proof;
- `lctk doctor` for a read-only Docker API probe.

These commands are foundation evidence, not the complete project lifecycle described elsewhere.

## Connecting a client

`lctk codex` generates the client configuration for a registered project and delivers its grant, following [ADR-0014](adr/0014-project-credential-delivery.md).

```sh
./lctk codex config PROJECT            # print the entry
./lctk codex config --apply PROJECT    # write it into CODEX_HOME/config.toml
./lctk codex launch PROJECT            # start the editor with the grant in its environment
./lctk codex status                    # where the entry is, and whether the variable is set
```

`config` writes only inside a marker-delimited region of a file LCTK does not own, takes a backup, and refuses to overwrite a same-named entry it did not generate. No generated file contains a token.

`launch` is how the credential arrives. Codex reads the token from an environment variable, and an editor that is already running keeps the environment it started with, so the editor must be closed first. To set the variable durably instead, `lctk codex env --reveal PROJECT` prints the command; LCTK does not run it.

## The code-intel service

[`images/code-intel`](../images/code-intel) is a separate Go module that builds the per-project search service. It links Zoekt, whose low-level index package is Unix-specific, so it is excluded from the root module's build on purpose and never compiles into the host executable.

That also means the ordinary checks above never touch it. It has its own CI job on Linux, and locally it is built and tested inside a container:

```sh
docker run --rm -v "$PWD/images/code-intel:/src" -w /src -e CGO_ENABLED=0 golang:1.25 go test ./...
```

`lctk image build` compiles it into the reusable image. A project must be restarted after a rebuild to pick up the new service.

## Dependency policy

Direct dependencies are pinned in `go.mod`, and checksums are committed in `go.sum`. Prefer the standard library. A new dependency requires a concrete capability, license review, maintenance review, and documentation update when it changes an architectural contract.
