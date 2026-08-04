# Symbols and syntax

LCTK answers two questions about a source file without a compiler, an interpreter, or the project's installed dependencies: **what does this file declare**, and **does it parse**.

Both come from a real syntax tree. The engine is Tree-sitter, chosen on measured evidence in [Slice 4.1](spikes/symbol-backend-evaluation-results.md) and recorded in [ADR-0019](adr/0019-tree-sitter-symbol-layer.md).

## What an outline is a claim about

`file_outline` reads the file from the read-only project mount and parses it. It does not consult the search index, so:

- there is nothing to flush and no debounce window to wait out — a file saved a moment ago is described correctly;
- the answer carries a **content digest** rather than an index generation, because there is no generation it could be behind;
- freshness is not reported, because the concept does not apply. The answer describes the bytes named by that digest.

This is a stronger claim than a search result makes, and it is stronger for the same reason: a search answers from an index that lags the disk by a debounce window, while an outline answers from the disk.

What it is **not** a claim about is unchanged from [ADR-0018](adr/0018-the-index-describes-the-disk.md): an edit that has not been written to disk does not exist here either, whoever is holding it.

## What a declaration carries

| | |
|---|---|
| `name` | the identifier as written |
| `kind` | `function`, `method`, `type`, `interface`, `struct`, `enum`, `class`, `field`, `constant`, `variable`, `module`, `macro`, or `other` |
| `start_line`, `end_line` | 1-based and inclusive |
| `start_byte`, `end_byte` | the declaration's extent, so it can be shown rather than located |
| `container` | the enclosing declaration's name, empty at the top level |
| `depth` | how deeply nested, so a flat list renders as a tree |
| `signature` | the declaration's own first line, trimmed and bounded to 200 bytes |

The vocabulary is LCTK's, not a grammar's. A caller should not have to learn one parser's node names, and a kind no configured grammar can produce is not in the list.

**Containment is computed from the extents, not from a name path.** A declaration is inside another when its bytes are. That is what makes a constant declared inside a method report that method as its container: nothing in the constant's own syntax says where it lives.

**The extent is the declaration and excludes what precedes it.** A doc comment above a type is not part of the type. One consequence is worth knowing: in Go a `const` or `var` *spec* is the declaration, not the enclosing `const (...)` group, so a single `const Limit = 10` reports a signature of `Limit = 10` rather than `const Limit = 10`. The `kind` field already says which it is, and using the spec is what makes a grouped declaration report its members individually.

## Precision

Every answer states `"precision": "syntax"`. It means exactly what it says:

- declarations are what the file's syntax says they are;
- nothing is type-resolved;
- nothing outside the file is consulted.

That is stated in the answer rather than only here, because a caller that reads a syntax-only answer as a type-resolved one will act on it.

## The syntax verdict

`syntax.valid` says whether the file parses. `syntax.errors` counts the broken *regions* — places to look, not the size of the damage — and `syntax.first_error_line` says where to start.

A file that does not parse still reports the declarations that did. An agent asking about the file it is midway through editing is the ordinary case, not an unusual one.

**The verdict is published per language, and `syntax.reported: false` means unknown rather than fine.** That distinction is not caution for its own sake. The C and C++ grammars have no preprocessor, and measured against real projects, 58 of 69 files from `zlib` and 134 of 438 from `nlohmann/json` parse with errors while compiling perfectly. Across 426 real files in Go, Python, Rust, JavaScript, and TypeScript, none did. Publishing a universal verdict would mean reporting a defect in most of a C project on the first look.

Anything beyond syntax belongs to the project's own toolchain. `build`, `test`, and `lint` already run in a container the project chose, under the policy in [ADR-0017](adr/0017-command-policy-and-the-runner.md). LCTK does not implement a second, worse type checker.

## Languages

A language with no configured grammar is **explicitly refused**, with `LANGUAGE_UNSUPPORTED` naming what this build does understand. An empty outline would read as "this file declares nothing", which is a different and wrong claim.

`project_info` reports `outline_languages`, so a caller learns the boundary by asking rather than by being refused on a file. It reports what the project's own service advertises, not what the host build expects: a project whose container predates the symbol layer answers nothing there, and `file_outline` is then not offered at all.

## What may be read

The store decides, using the same rules that decide what is indexed. That is one authority rather than two, and it is why an outline cannot reach a file the index would not hold:

- a path that is not project-relative is refused with `INVALID_PATH`;
- a symbolic link is refused rather than followed;
- version-control metadata is never readable;
- a file the project's ignore rules exclude answers `FILE_NOT_FOUND` — **the same answer as a file that is not there**, so the difference between two refusals cannot be used to map what exists outside the caller's scope.

## Bounds

| | |
|---|---|
| Per-file parse budget | 5 s, `LCTK_SYMBOL_BUDGET` |
| Largest file outlined | 4 MiB, `LCTK_SYMBOL_MAX_FILE_BYTES` |

The parse bound is wall clock rather than a byte count because the cost is not proportional to size: a pathological construct in a file of ordinary length is what holds a parser, and the container it would hold is the project's own. A file abandoned to the budget reports `PARSE_INCOMPLETE`, never an empty outline — "not analysed" and "declares nothing" must not arrive as the same answer.

The size bound is deliberately larger than the index's own 1 MiB file limit, because the two exist for different reasons. A large file costs the index space in every generation it appears in; an outline costs one parse and is not stored.
