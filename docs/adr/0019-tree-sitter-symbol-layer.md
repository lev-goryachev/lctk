# ADR-0019: Tree-sitter is the symbol layer, and diagnostics stop at syntax

- Status: accepted
- Date: 2026-08-04
- Deciders: project maintainers

## Context

Stage 4 adds symbol and AST intelligence for TypeScript/JavaScript, Python, Rust, C/C++, and Go: outlines, definitions, references, and diagnostics. Stage 5 then builds an AST-aware chunk model on the same layer.

Three constraints come from decisions already made and cannot be traded away here.

The project source is mounted **read-only** and LCTK installs nothing into it ([ADR-0003](0003-reusable-images-and-project-stacks.md), [project lifecycle](../project-lifecycle.md)). One reusable image serves every project, so nothing language-specific can be provisioned per project. And a client's answer must be honest about its own precision: an index describes the disk and freshness is never optimistic ([ADR-0015](0015-change-observation-is-complete-or-declared-incomplete.md), [ADR-0018](0018-the-index-describes-the-disk.md)).

Slice 4.1 evaluated Tree-sitter and Universal Ctags under one tracked contract against real source from each language's own project. The [measured results](../spikes/symbol-backend-evaluation-results.md) record the run.

A language server was excluded before measurement, for a reason that is a constraint rather than a preference. `tsserver`, `pyright`, `rust-analyzer`, `clangd`, and `gopls` each reach their stated precision only with the project's own dependencies resolved — `node_modules`, an environment, a Cargo registry, a compilation database. On a read-only mount with nothing installed, a language server answers at a precision it cannot actually reach, while costing a per-language toolchain in a shared image and a per-language process to supervise, restart, and bound. Running one is a separate decision with its own ADR; it is not the way to get outlines.

## Decision

**Use Tree-sitter as LCTK's symbol and AST engine**, linked through cgo into the per-project Linux service, with LCTK-owned extraction queries per language. Pin the core and every grammar to exact versions.

The engine boundary follows [ADR-0011](0011-zoekt-exact-search-backend.md) exactly: this code lives in the nested `images/code-intel` module and never enters the portable host executable. Adopting cgo revises that image's stated position that no C toolchain enters the build; the runtime stage remains one static binary with no toolchain present, which is what that position was protecting.

**A symbol is what the syntax says it is, and resolution across files is by name.** A declaration carries a name, a kind, a line range, a byte range, and its enclosing declaration, all taken from the tree's own extents. A cross-file definition or reference is matched on the identifier and is reported as name-based, because nothing here resolves types. That answer is useful and it is honest; presenting it as a type-resolved answer would not be.

**Diagnostics stop at syntax, and only where syntax is trustworthy.** LCTK reports whether a file parses. It does not implement a type checker, a linter, or a compiler front end: a project's own `build`, `test`, and `lint` commands already run in a container the project chose, under the policy in [ADR-0017](0017-command-policy-and-the-runner.md), and that is where a toolchain's opinion belongs.

The syntax verdict is offered per language rather than universally. The measurement is the reason: the C and C++ grammars have no preprocessor, so 58 of 69 real C files and 134 of 438 real C++ files parse with errors while compiling perfectly, against zero of 426 files across Go, Python, Rust, JavaScript, and TypeScript. A verdict is therefore published for the five languages where it means something and withheld for C and C++, where it would report a defect in most of a project on the first look.

**A language with no grammar configured is explicitly unsupported, not answered less precisely.** No second engine is added to widen coverage. Two answers of different precision behind one tool is worse for a calling agent than one answer with a stated boundary, and "gap over guess" is already how this repository treats an incomplete observation.

**Every parse is bounded.** The per-file budget uses Tree-sitter's cancellation callback, and a file abandoned to it is reported as unanalysed rather than as having no symbols.

## Alternatives considered

### Universal Ctags

Rejected. It fails two hard gates, neither of which is a matter of configuration.

It produces **no byte ranges** — 0 of 27,064 symbols. Stage 5's chunk model needs boundaries in bytes, and so does answering "show me this declaration". Reconstructing them from line numbers means deciding where a declaration ends, which is a parser.

It **cannot report a broken file** — 0 of 7 truncated constructs. This is not a defect but a property: a tag generator reports what it recognized, and a half-written file yields fewer tags, which is indistinguishable from a file that declares less. That removes the one diagnostic LCTK can offer without a toolchain.

It also gives no end line at all for JavaScript, TypeScript, or Rust, and found 24 of 137 declarations in `axios/index.d.ts` — the public type surface of a widely used library, in the first language Stage 4 names.

What it is better at is recorded rather than dismissed: twice as fast, a fifth of the memory, no cgo, one package in the image, and 148 languages out of the box against seven grammars. If breadth across languages LCTK has no grammar for becomes a requirement, that is a new decision and this ADR is where it starts.

Its GPL-2.0 licence was not the reason. A separately executed binary in an image alongside busybox is aggregation, not linking.

### A language server per language

Excluded before measurement, on the constraints above. Recorded here because it is the obvious suggestion and the reason not to do it is specific: a read-only mount with no installed dependencies is not an environment where `rust-analyzer` or `clangd` can answer at the precision their presence would imply.

### Go's own `go/ast` for Go, and something else per language

Rejected. It is exact and free for Go, and it makes Go the only language with a different implementation, a different vocabulary, and different failure modes behind the same tool. Uniformity is worth more here than exactness in one language, and the Go measurement shows the grammar is not the weak point: 3,230 symbols across 127 files with zero parse errors and zero files producing nothing.

### Zoekt's built-in ctags integration

Rejected. `DisableCTags` stays set. Enabling it would put symbol metadata inside the search index, where it is a ranking signal rather than a queryable structure, and it inherits every ctags limitation above.

## Consequences

### Positive

- One engine answers every named language, with the same vocabulary and the same failure modes.
- A declaration is bounded in bytes, so Stage 5's chunk model has boundaries to use and a caller can be shown a declaration rather than a cursor position.
- Containment is exact, computed from the tree's extents rather than from a name path.
- A syntax diagnostic exists at all, for the five languages where it is trustworthy, with no toolchain and no dependency resolution.
- Nothing in the analysis path touches the filesystem: it parses bytes, so it works on a read-only mount and on content the index already holds.
- No per-language process to supervise, restart, or bound. The existing container lifecycle and resource modes govern the symbol layer unchanged.
- The public tool schema stays engine-independent, as [ADR-0004](0004-stable-aggregated-tool-api.md) requires.

### Negative

- cgo and a C toolchain in the build stage, revising a stated position of the image.
- Eight modules to pin and upgrade deliberately, one core and seven grammars, each with its own cadence.
- About 11 MB added to the service binary and roughly a minute of cold build time, dominated by generated C.
- Peak memory roughly five times ctags' for the same corpus, because a syntax tree is not a tag list.
- No syntax verdict for C or C++, which is the honest outcome rather than a silent one.
- Cross-file answers are name-based. A caller that wants type-resolved references does not get them here.
- A language without a configured grammar is unsupported rather than partially served.
- `linux/arm64` is compiled but not executed. The grammars cross-compile in 27 s; an emulated run on the developer's machine exceeded ten minutes and was abandoned, so nothing yet shows the arm64 result working.

### Follow-up

- Close the query gaps the measurement named: preprocessor definitions for C and C++, module-level assignment for Python, module-level non-function bindings for JavaScript.
- State the name-based precision of a definition or reference answer in the answer itself, not only in documentation.
- Keep the symbol layer tied to the published index generation, so a symbol answer and a search answer never describe different content.
- Measure what the layer costs a real project under each resource mode before Stage 4 is declared complete.
- Execute the arm64 build rather than only compiling it, in the release pipeline, alongside the same open item ADR-0011 carries for the search engine.
- Revisit grammar pins deliberately; an ABI change in the core is a compatibility event, not a routine upgrade.
