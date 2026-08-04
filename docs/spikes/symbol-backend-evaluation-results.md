# Symbol backend evaluation results

Measured 2026-08-04 against the [evaluation contract](symbol-backend-evaluation.md) with the harness in [`spikes/symbol-backend-evaluation/`](../../spikes/symbol-backend-evaluation/).

Outcome: **Tree-sitter is selected.** Universal Ctags fails two hard gates outright. The decision is recorded in [ADR-0019](../adr/0019-tree-sitter-symbol-layer.md).

## Corpus

Resolved commits, cloned by the harness:

| Project | Tag | Commit |
|---|---|---|
| `psf/requests` | `v2.32.3` | `0e322af87745eff34caffe4df68456ebc20d9068` |
| `BurntSushi/ripgrep` | `14.1.1` | `4649aa9700619f94cf9c66876e9549d83420e16c` |
| `madler/zlib` | `v1.3.1` | `51b7f2abdade71cd9bb0e7a373ef2610ec6f9daf` |
| `nlohmann/json` | `v3.11.3` | `9cca280a4d0ccf0c08f47a99aa71d1b0e52f8d03` |
| `axios/axios` | `v1.7.7` | `5b8a826771b77ab30081d033fdba9ef3b90e439a` |
| this repository | — | `20582882e9115f9ab97df76378aec304ac246f66` |

933 files, 10,323 KiB.

## Hard gates

| Gate | Tree-sitter | Universal Ctags |
|---|---|---|
| 1. No toolchain, read-only source | pass | pass |
| 2. Every named language | pass | pass |
| 3. Name, kind, line range, container | pass | **partial** — no end line at all for JavaScript, TypeScript, or Rust |
| 4. Byte ranges | pass — 16,466 of 16,466 symbols | **fail** — 0 of 27,064 |
| 5. A broken file is reported as broken | pass — 7 of 7 | **fail** — 0 of 7 |
| 6. Per-file work | pass | pass |
| 7. Bounded per file | pass | **partial** — see the stall below |
| 8. Licence and architecture | licence pass, MIT; **arm64 compiled, not executed** | licence pass, GPL-2.0 as a separate executable; arm64 packaged upstream |

Gates 4 and 5 are the ones that decide it, and neither is a matter of tuning.

Gate 8 is a partial pass and is written that way on purpose. The eight grammars **cross-compile** for `linux/arm64` with cgo in 27 s, producing a 12,064,656-byte binary. They were not **run** on arm64: an emulated build on this machine exceeded ten minutes and was abandoned, so nothing here shows the result executing. That verification belongs to the release pipeline, alongside the same open item [ADR-0011](../adr/0011-zoekt-exact-search-backend.md) already carries for the search engine.

## Coverage and cost

Tree-sitter, per-file budget 5 s:

| Language | Files | KiB | Symbols | Nested | Byte ranges | End line | No symbols | Unparsed | ms |
|---|---|---|---|---|---|---|---|---|---|
| c | 69 | 1697 | 1436 | 567 | 1436 | 652 | 4 | 58 | 1246 |
| cpp | 438 | 4780 | 5921 | 3069 | 5921 | 3206 | 30 | 134 | 4888 |
| go | 127 | 1022 | 3230 | 1514 | 3230 | 1473 | 0 | 0 | 874 |
| javascript | 159 | 753 | 883 | 273 | 883 | 563 | 83 | 0 | 695 |
| python | 36 | 367 | 753 | 546 | 753 | 753 | 6 | 0 | 243 |
| rust | 98 | 1654 | 4069 | 1212 | 4069 | 3047 | 4 | 0 | 1386 |
| typescript | 6 | 47 | 174 | 83 | 174 | 66 | 3 | 0 | 49 |

**933 files, 16,466 symbols, 9.9 s, peak RSS 68 MiB.** No file hit the budget. Worst single file 708 ms: `json/single_include/nlohmann/json.hpp`, a 197 KiB single-header library.

Universal Ctags, one process per file, same budget:

| Language | Files | Symbols | Nested | Byte ranges | End line | No symbols | ms |
|---|---|---|---|---|---|---|---|
| c | 69 | 2102 | 516 | 0 | 724 | 2 | 421 |
| cpp | 438 | 12067 | 5273 | 0 | 4482 | 10 | 1991 |
| go | 127 | 3008 | 2879 | 0 | 1427 | 0 | 605 |
| javascript | 159 | 4188 | 2344 | 0 | 0 | 16 | 725 |
| python | 36 | 910 | 558 | 0 | 753 | 2 | 182 |
| rust | 98 | 4666 | 3395 | 0 | 0 | 5 | 479 |
| typescript | 6 | 123 | 25 | 0 | 0 | 1 | 31 |

**933 files, 27,064 symbols, 5.0 s, peak RSS 12 MiB.**

Ctags is about twice as fast and a fifth of the memory, and it finds more names. Both are true and neither changes the outcome: it produces no byte ranges, no end line for three of the seven languages, and no way to say a file is broken. It is a tag generator, and a tag is a jump target rather than a description of a declaration.

## The two gates ctags fails

**Byte ranges: 0 of 27,064.** A tag carries a line and sometimes an end line. Stage 5's chunk model needs boundaries in bytes, and so does any answer of the form "show me this function". Reconstructing them from lines means re-reading the file and guessing where a declaration stops, which is a parser — the thing the engine was supposed to provide.

**A broken file: 0 of 7.** Every language's truncated construct was reported as whole:

```
language    tree-sitter            universal-ctags
go          parsed true -> false   parsed true -> true
python      parsed true -> false   parsed true -> true
rust        parsed true -> false   parsed true -> true
c           parsed true -> false   parsed true -> true
cpp         parsed true -> false   parsed true -> true
javascript  parsed true -> false   parsed true -> true
typescript  parsed true -> false   parsed true -> true
```

This is not a defect in ctags. It has no concept of failure: it reports what it recognized, and a half-written file simply yields fewer tags — indistinguishable from a file that declares less. An agent cannot act on that, and "your edit does not parse" is the one diagnostic LCTK can offer without a toolchain.

## What only real code showed

**Tree-sitter reports 58 of 69 C files and 134 of 438 C++ files as containing syntax errors — and they are all valid code.** The grammars have no preprocessor, so `#if`, macro-generated declarations, and macros used as type qualifiers produce `ERROR` nodes in files that compile perfectly. Across the other 426 real files — Go, Python, Rust, JavaScript, TypeScript — **zero** were reported as broken.

So the mechanism passes gate 5 and the *diagnostic* is only trustworthy per language. A syntax verdict must be offered for Go, Python, Rust, JavaScript, and TypeScript, and withheld for C and C++, where LCTK would otherwise report a defect in most of the project on the first look. A fixture would never have shown this: a hand-written C fixture parses.

**Ctags' interactive protocol stalls on ordinary JavaScript.** Driving one warm process and writing each file's content to it is how Zoekt uses ctags and is the configuration a service would want. It stopped replying on `axios/bin/GithubAPI.js`, 3,573 bytes, after emitting 18 tags. The same file through the ordinary command line takes 40 ms and produces 24 tags, along with `ctags: Warning: ignoring null tag`, which is where the protocol appears to lose its framing. Reproduced with a minimal independent driver — not the harness — against **Universal Ctags 5.9.0 and 6.1.0**, and separately on `axios/dist/esm/axios.js`, where it stalled after 468 and 658 tags respectively.

No corpus-wide figure is claimed for that mode: the harness could not complete a run in it, and the per-file numbers above are ctags measured in its working configuration. The operational point stands on its own — the protocol offers no way to abandon one file, so recovery means killing the warm process that was the reason to use the mode at all.

**Unbounded parsing is a hazard worth designing against.** No corpus file exceeded 708 ms, so the budget never fired here. It stays because the cost is not proportional to size: a pathological construct in a file of ordinary length is what holds a parser, and the container it would hold is the project's own. Tree-sitter has a documented cancellation callback; the harness uses it.

## Where the two disagree

Names found by one engine and not the other, per language:

| Language | Files | Both | Only tree-sitter | Only ctags |
|---|---|---|---|---|
| c | 69 | 965 | 218 | 898 |
| cpp | 438 | 3199 | 343 | 2554 |
| go | 127 | 2711 | 290 | 127 |
| javascript | 159 | 685 | 100 | 2938 |
| python | 36 | 690 | 0 | 150 |
| rust | 98 | 2601 | 114 | 263 |
| typescript | 6 | 41 | 114 | 65 |

Reading the cases matters more than the totals, and they fall into three groups.

**Gaps in LCTK's own queries, which this measurement exists to find.** The C and C++ excess is almost entirely preprocessor macros — `zlib/adler32.c` gives ctags `BASE`, `DO1`, `DO2`, `DO4`, `DO16` and tree-sitter none, because the query has no `preproc_def` pattern. Python's is module-level assignment: `requests/docs/conf.py` is 34 names to ctags and **zero** to tree-sitter, since the query covers only functions and classes. JavaScript's is module-level `const` that is not a function. All three are patterns to add, and they are now written down rather than guessed at.

**Things that are not declarations.** `__dirname`, `AnonymousFunction8fdaa8540100`, and the `main` that ctags reports for every Go file's package clause.

**A real gap in ctags.** `axios/index.d.ts` — the public type surface of a widely used library — has 137 declarations to tree-sitter and 24 to ctags. Interfaces and type aliases are most of what a `.d.ts` file *is*, and TypeScript is the first language named in Stage 4.

Rust runs the other way: tree-sitter finds test functions and constants nested inside modules that ctags misses, 39 against 31 in `ripgrep/crates/cli/src/decompress.rs`.

## What Tree-sitter costs

- **cgo, and a C toolchain in the build.** The image's build stage says CGO is off so no C toolchain enters the supply chain; adopting tree-sitter revises that. The final stage stays a single static binary: an eight-grammar probe linked statically against musl on `golang:1.25-alpine` came to **10,920,488 bytes** and ran on plain `alpine:3.22` with no toolchain present. The shipped service binary is currently 32,194,744 bytes in a 55.7 MB image.
- **51 s of build time** for the eight grammars, cold, in that probe. They are generated C and they dominate a rebuild, so the build caches stop being optional.
- **Eight modules to pin**, one core and seven grammars, each with its own release cadence. Grammar ABI 14 and 15 are both accepted by bindings v0.25.0, which is what allows the set to be pinned at mixed versions.
- **Memory: 68 MiB peak** against ctags' 12 MiB, for a full syntax tree rather than a tag list.

## Interpretation limits

One machine, one container, 10 MiB of source. The figures rank the engines against each other on this corpus; they are not a claim about a million-file repository.

The queries are LCTK's own and the gaps named above are real. They under-report C, C++, Python, and JavaScript in the tree-sitter column, so the symbol counts are not a ceiling for either engine.

Enumerating the corpus through a Windows bind mount took longer than analysing it, which made the first attempt a measurement of the filesystem rather than of either engine. The corpus lives in a Docker volume for that reason, and the harness's README says so.
