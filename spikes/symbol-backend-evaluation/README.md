# Symbol backend evaluation harness

The tracked, evidence-only harness for [Slice 4.1](../../docs/roadmap.md#slice-41-choosing-the-symbol-engine). It is not production LCTK code and it defines no public tool schema. The contract it answers is
[`docs/spikes/symbol-backend-evaluation.md`](../../docs/spikes/symbol-backend-evaluation.md) and the measured outcome is
[`docs/spikes/symbol-backend-evaluation-results.md`](../../docs/spikes/symbol-backend-evaluation-results.md).

## Pins

- Go 1.25.9, build image `golang:1.25.9-bookworm` at `sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3`
- `go-tree-sitter` v0.25.0; grammars `go` v0.25.0, `python` v0.25.0, `javascript` v0.25.0, `rust` v0.24.2, `c` v0.24.2, `cpp` v0.23.4, `typescript` v0.23.2
- Universal Ctags as packaged in the build image

## Running it

Both candidates need Linux — tree-sitter through cgo, Universal Ctags as a native executable — so the Dockerfile is the execution boundary.

```bash
docker build -t lctk-symbol-eval:4.1 spikes/symbol-backend-evaluation
```

The corpus belongs in a Docker volume rather than a bind mount. That is not a preference: enumerating the corpus and this repository through a Windows bind mount took longer than analysing either, which made the first measurement a measurement of the filesystem.

```bash
docker volume create lctk-symbol-eval
docker run --rm -v lctk-symbol-eval:/evidence lctk-symbol-eval:4.1 corpus --dir /evidence/corpus
```

Add this repository as the Go corpus by cloning it into the same volume, so the Go measurement is of a real commit rather than of a working tree that changes between runs.

```bash
docker run --rm --entrypoint git -v lctk-symbol-eval:/evidence -v "$PWD:/lctk:ro" \
  lctk-symbol-eval:4.1 clone --quiet /lctk /evidence/corpus/lctk
```

Then measure, compare, and check the syntax gate:

```bash
docker run --rm -v lctk-symbol-eval:/evidence lctk-symbol-eval:4.1 measure --engine tree-sitter --budget 5s --go-root /evidence/corpus/lctk
```

`--json` emits the report as JSON. `--verbose` names each file before analysing it, which is how a stall is attributed to a file rather than guessed at.

## Commands

- `corpus` clones the pinned projects and prints the resolved commit for each;
- `measure` runs one engine over the corpus and reports coverage and cost;
- `compare` runs both and reports, per language, the names only one of them found;
- `broken` reports whether each engine can tell a truncated file from a whole one.

`--ctags-mode` selects how Universal Ctags is driven: `per-file` starts a process per file, `interactive` keeps one warm process. Both are implemented because they do not behave the same, and the difference is one of the results.

## What is real

- both engines, at pinned versions, in the configuration a production service would use;
- real source from each language's own project, pinned by tag;
- the extraction queries a production symbol layer would carry, not simplified stand-ins;
- one shared per-file budget, so neither engine is credited with finishing a file the other was stopped on.

Nothing is simulated, and nothing the harness computes itself is credited to a candidate.

## Interpretation limits

Timings come from one machine over about 10 MiB of source. They rank the engines against each other on this corpus and say nothing about a million-file repository.

A query with a gap under-reports for its language. The "files without symbols" column and the `compare` command exist to expose that rather than to hide it.
