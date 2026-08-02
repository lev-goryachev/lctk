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
- `lctk watch-once <directory>` for a raw single-event probe on any directory, registered or not; the project watcher is `lctk project watch --follow`;
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

## Watching a project

A running daemon watches each running project and records what it sees in a per-project change journal. See [indexing](indexing.md#the-change-journal) for what the journal claims and [ADR-0015](adr/0015-change-observation-is-complete-or-declared-incomplete.md) for why.

```sh
./lctk project watch PROJECT             # what the daemon has recorded
./lctk project watch --follow PROJECT    # stream normalized events, writing nothing
./lctk settings show                     # the debounce and watch policy in force
```

`watch` reads the journal from disk, so it answers even when no daemon is running — which is when the last recorded state is most worth seeing. `--follow` starts a watcher of its own for diagnosis and does not write to the journal.

A running daemon applies what it observes to the index by itself, so `lctk project reindex` is no longer part of an editing loop. It remains useful without a daemon, and it is the documented recovery for a corrupt index.

The machine policy lives in `settings.json` in the LCTK home, and `settings show` prints its path whether or not the file exists:

```json
{
  "schema_version": 1,
  "watch": {
    "debounce_ms": 3000,
    "max_debounce_ms": 30000,
    "max_watched_directories": 20000,
    "idle_stop_seconds": 900
  }
}
```

A project may propose its own window with `index.debounce_ms` in the manifest. The host clamps it.

## What a project is allowed to cost

```sh
./lctk project resources PROJECT                 # mode, limits, and disk use
./lctk project resources --mode quiet PROJECT    # set this project's mode
./lctk project resources --mode default PROJECT  # follow the machine again
```

Modes are `quiet`, `normal`, and `fast`; see [indexing](indexing.md#resource-modes) for what each costs. Limits are applied when a container is created, so a change takes effect at the next restart.

`start` and `restart` refuse when the volume is short of space, and `--yes` overrides that.

## Git tools

A running project serves `git_status` and `git_diff` alongside `project_info` and `exact_search`. They are read-only, bound to the project by the route, and they exist for the client that has no shell on the machine; an editor's own terminal does not need them.

`git_status` reports the branch, commit, upstream position, and changed paths. `git_diff` returns a bounded unified diff, optionally of what is staged and optionally restricted to given paths. Paths are repository-relative, and `prefix` says where the project sits inside the repository when the two differ.

## The admin page

```sh
./lctk admin open            # open the local admin page, signed in once
./lctk admin open --print    # print the link instead
```

The link carries a code that is spent the moment the page uses it, and the page clears it from the address bar. A new code is issued on every daemon start and removed when the daemon stops, so signing in again after a restart means running the command again. See [ADR-0016](adr/0016-admin-surface-and-local-session.md).

## The code-intel service

[`images/code-intel`](../images/code-intel) is a separate Go module that builds the per-project search service. It links Zoekt, whose low-level index package is Unix-specific, so it is excluded from the root module's build on purpose and never compiles into the host executable.

That also means the ordinary checks above never touch it. It has its own CI job on Linux, and locally it is built and tested inside a container:

```sh
docker run --rm -v "$PWD/images/code-intel:/src" -w /src -e CGO_ENABLED=0 golang:1.25 go test ./...
```

`lctk image build` compiles it into the reusable image. A project must be restarted after a rebuild to pick up the new service.

## Dependency policy

Direct dependencies are pinned in `go.mod`, and checksums are committed in `go.sum`. Prefer the standard library. A new dependency requires a concrete capability, license review, maintenance review, and documentation update when it changes an architectural contract.
