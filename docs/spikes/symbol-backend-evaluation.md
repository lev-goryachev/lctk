# Symbol backend evaluation contract

## Status

Accepted Slice 4.1 test contract. Engine selection remains open until the measured results are reviewed.

Evaluation date: 2026-08-04.

## What is being chosen

Stage 4 adds symbol and AST intelligence: outlines, definitions, references, and diagnostics, for TypeScript/JavaScript, Python, Rust, C/C++, and Go. Stage 5 then needs an AST-aware chunk model built on the same layer.

One engine must answer all of it, or LCTK ends up with two answers of different precision behind one tool, which is worse for a calling agent than a single honest one.

## Candidates

- **Tree-sitter**, core `github.com/tree-sitter/go-tree-sitter` v0.25.0, with grammars `tree-sitter-go` v0.25.0, `tree-sitter-python` v0.25.0, `tree-sitter-javascript` v0.25.0, `tree-sitter-rust` v0.24.2, `tree-sitter-c` v0.24.2, `tree-sitter-cpp` v0.23.4, `tree-sitter-typescript` v0.23.2.
- **Universal Ctags**, as packaged for the image, driven two ways: one warm process over the interactive protocol, and one process per file.

A language server is deliberately **not** a candidate for this slice, and the reason is a hard constraint rather than a preference. `tsserver`, `pyright`, `rust-analyzer`, `clangd`, and `gopls` each need the project's own dependencies resolved to answer at their stated precision — `node_modules`, an environment, a Cargo registry, a compilation database. LCTK mounts the source **read-only** and installs nothing, so a language server here would answer at a precision it cannot reach while costing a per-language toolchain and a per-language process lifecycle. Whether LCTK ever runs one is a separate decision with its own ADR; it is not the way to get outlines.

## Hard gates

A candidate that fails any of these cannot be selected.

1. **No toolchain, no install, read-only source.** The engine answers from the bytes of the mounted project and nothing else. No compiler, interpreter, package manager, or dependency resolution.
2. **Every named language.** TypeScript, JavaScript, Python, Rust, C, C++, and Go.
3. **A symbol carries a name, a kind, a line range, and its enclosing declaration.** A bare name and line number is a cursor position, not an outline.
4. **Byte ranges.** A declaration is bounded in bytes, not only in lines, because Stage 5's chunk model needs boundaries and a line is not one.
5. **A broken file is reported as broken.** An engine that answers "fewer symbols" for a half-written file cannot support a syntax diagnostic, because fewer symbols is indistinguishable from a file that declares less.
6. **Per-file work.** One changed file is re-analysed without touching the others, so the symbol layer can follow the existing incremental index rather than forcing a rebuild.
7. **Bounded per file.** One pathological file must not hold the project's service. The engine must offer a way to abandon one file that does not destroy the state used for every other file.
8. **Apache-2.0-compatible distribution**, and buildable for `linux/amd64` and `linux/arm64`.

## Corpus

Real source from each language's own project, pinned by tag, cloned by the harness. A fixture is not admissible evidence here: it would be written by whoever writes the extraction queries, so it can only confirm that the queries match their author's expectations. Real code carries the constructs nobody remembers to write down — macros, conditional compilation, generics, decorators, generated bundles, and minified output.

| Language | Project | Tag |
|---|---|---|
| Python | `psf/requests` | `v2.32.3` |
| Rust | `BurntSushi/ripgrep` | `14.1.1` |
| C | `madler/zlib` | `v1.3.1` |
| C++ | `nlohmann/json` | `v3.11.3` |
| JavaScript and TypeScript | `axios/axios` | `v1.7.7` |
| Go | this repository | the commit under test |

`ripgrep` is here for a second reason: it is already this repository's declared correctness oracle for exact search, so the Rust corpus is code the project has committed to trusting elsewhere.

Test directories, generated bundles, and vendored code are **kept**. Excluding them would measure the friendliest part of each project, and unusual syntax lives in exactly those places.

## Measurements

Per engine, per language:

- files, bytes, and symbols;
- symbols reported inside another declaration, which is the evidence for gate 3 rather than the claim;
- symbols bounded in bytes, and symbols whose end line differs from the start;
- files that produced no symbols at all, which is either a language the engine handles badly or a gap in the extraction;
- files the engine considers syntactically broken;
- files abandoned to the budget, and files that failed outright;
- wall time in total and for the worst single file.

Both candidates are bounded by the same per-file budget, so neither is credited with finishing a file the other was stopped on.

Peak resident memory is measured per engine in its own process, so the figure is attributable.

## Agreement

The two engines are also run over the same corpus together, reporting per language how many names both found and how many only one found, with examples. Neither is the oracle: the point is to locate where they disagree and to read those cases, because a name only one engine finds is either a gap in the other or a thing that is not a declaration.

## Syntax validity

Each language contributes a whole construct and the same construct truncated mid-body — the shape of a half-written edit, which is the case that matters, rather than random bytes. An engine passes gate 5 only by reporting the whole file as whole and the damaged one as damaged.

## Interpretation limits

Timings are from one machine, inside a container, over a corpus of about 10 MiB. They characterize the engines relative to each other on this corpus and are not a claim about a million-file repository.

The extraction queries are LCTK's own and are the ones a production symbol layer would carry. A query with a gap in it under-reports for that language, which the "files without symbols" and agreement columns exist to expose. The queries are not tuned per project.

The harness is evidence. It defines no public tool schema and becomes no part of the shipped service.
