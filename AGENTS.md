# Project Instructions

## Language

- Write all project documentation in English. This includes the root README, files under `docs/`, ADRs, contribution guides, release notes, and user-facing examples.
- Write all source-code comments and doc comments in English.
- Use English for identifiers, schemas, configuration keys, command help, logs, diagnostics, errors, and other user-facing text unless localization is being implemented explicitly.
- When changing an existing non-English document or comment, translate the affected content to English rather than extending it in another language.
- User conversations may use the user's preferred language; this rule applies to repository content.

## Documentation and decisions

- Treat `docs/` as the source of truth for product and architecture requirements.
- Record significant long-term architectural decisions as ADRs under `docs/adr/`.
- Distinguish accepted decisions, working assumptions, and open questions. Do not present an unverified integration as working.
- Update the relevant documentation in the same change as behavior or architectural contracts.

## Workspace boundary

- Treat the current repository root as the hard boundary for all write operations.
- Do not create, modify, move, or delete files or directories outside the repository root unless the user explicitly approves the exact external path and operation in the current conversation.
- Do not use the operating-system temporary directory, user profile, editor storage, or another drive for downloads, generated files, proof spikes, toolchains, caches, or intermediate artifacts. Keep task artifacts under an ignored directory inside the repository and remove them when they are no longer needed.
- Reading outside the repository is allowed only when necessary for explicitly provided system documentation or installed-tool metadata. Never write back to those locations.
- Terminal commands must use the repository as their working directory and must not redirect, copy, download, extract, or otherwise write outside it without explicit user approval.
- If a tool requires an external write to proceed, stop and ask for approval before invoking it. Technical access to an external path is not permission to use it.

## Delivery

- Deliver small, testable vertical slices that belong to the target architecture; avoid disposable shortcuts presented as production foundations.
- Preserve server-enforced project scope, project-specific persistent state, stable public MCP APIs, and the separation between coding tools and Docker administration.
- Preserve the Apache-2.0 licensing decision and keep required notices accurate.
- Do not accept external pull requests until contribution intake opens in roadmap Slice 1.5.
