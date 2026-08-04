# Third-Party Notices

LCTK's current direct Go dependencies are:

- `github.com/modelcontextprotocol/go-sdk v1.6.1`;
- `github.com/fsnotify/fsnotify v1.10.1`;
- `github.com/moby/moby/client v0.5.0`.

Their transitive versions and module checksums are recorded in `go.mod` and `go.sum`.

The tracked Slice 0.3 evidence module under `spikes/search-backend-evaluation` directly depends on:

- `github.com/sourcegraph/zoekt v0.0.0-20260724095353-2b2ce2e398e6` (Apache-2.0).

Its transitive versions and checksums are isolated in that directory's `go.mod` and `go.sum`. The harness is research evidence, not part of the current `lctk` executable.

The tracked Slice 4.1 evidence module under `spikes/symbol-backend-evaluation` directly depends on:

- `github.com/tree-sitter/go-tree-sitter v0.25.0` (MIT), which vendors the Tree-sitter C library (MIT);
- `github.com/tree-sitter/tree-sitter-go v0.25.0`, `tree-sitter-python v0.25.0`, `tree-sitter-javascript v0.25.0`, `tree-sitter-rust v0.24.2`, `tree-sitter-c v0.24.2`, `tree-sitter-cpp v0.23.4`, and `tree-sitter-typescript v0.23.2` (each MIT);
- `github.com/mattn/go-pointer v0.0.1` (MIT).

It also drives Universal Ctags (GPL-2.0) as a separate executable installed in the harness image. Ctags is a candidate under evaluation, not a dependency of anything LCTK distributes, and the [decision](docs/adr/0019-tree-sitter-symbol-layer.md) does not select it.

Each dependency remains subject to its own license. This file will contain the generated complete attribution inventory before the first distributed release. Current workflow archives are non-publishing dry runs; the pre-alpha repository does not yet publish a GitHub Release, production binary, or container image.
