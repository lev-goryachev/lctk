# Third-Party Notices

LCTK's current direct Go dependencies are:

- `github.com/modelcontextprotocol/go-sdk v1.6.1`;
- `github.com/fsnotify/fsnotify v1.10.1`;
- `github.com/moby/moby/client v0.5.0`.

Their transitive versions and module checksums are recorded in `go.mod` and `go.sum`.

The tracked Slice 0.3 evidence module under `spikes/search-backend-evaluation` directly depends on:

- `github.com/sourcegraph/zoekt v0.0.0-20260724095353-2b2ce2e398e6` (Apache-2.0).

Its transitive versions and checksums are isolated in that directory's `go.mod` and `go.sum`. The harness is research evidence, not part of the current `lctk` executable.

Each dependency remains subject to its own license. This file will contain the generated complete attribution inventory before the first distributed release. Current workflow archives are non-publishing dry runs; the pre-alpha repository does not yet publish a GitHub Release, production binary, or container image.
