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

## Dependency policy

Direct dependencies are pinned in `go.mod`, and checksums are committed in `go.sum`. Prefer the standard library. A new dependency requires a concrete capability, license review, maintenance review, and documentation update when it changes an architectural contract.
