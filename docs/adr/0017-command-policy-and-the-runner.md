# ADR-0017: A repository proposes commands, the owner approves them, a client runs them by name

- Status: accepted
- Date: 2026-08-02
- Deciders: project maintainers

## Context

Stage 3 calls for a constrained runner and a "typed command policy generated through validated manifest". [`docs/product.md`](../product.md) is more specific: the user confirms a command before it becomes runnable policy.

Everything LCTK had built until now reads. The runner is the first part that executes somebody's code, and that changes the trust question completely. A repository manifest is untrusted input — [`docs/security.md`](../security.md) already says so, which is why the manifest cannot mount a path, grant a capability, or supply a secret. But a manifest *may* propose build, test, and lint commands, and a command is code.

Two failures have to be prevented, and they are different. A client must not be able to run something nobody agreed to. And a repository must not be able to change what "the tests" means after somebody agreed to it.

## Decision

### Three parties, three roles

The repository **proposes**. The machine owner **approves**. The client **runs, by name only**.

`run_command` takes `build`, `test`, or `lint` and nothing else. It has no parameter that carries a command line. A client therefore cannot execute anything outside the set a human read and agreed to, and the set cannot be extended from the client side at all.

The vocabulary is closed on purpose. A fixed three lets a client ask for "the tests" while knowing nothing about how this project runs them, and it keeps a manifest from inventing a command surface nobody reviewed.

### Approval is bound to the exact text

An approval stores the SHA-256 of the command it approved, and nothing else about it. If the manifest later changes `test` from `go test ./...` to anything else, the digest no longer matches and the command is refused until a person approves it again.

This is the second failure, and it is the more interesting one. Without it, a repository could offer a harmless command, wait for approval, and then quietly replace it — every approval would be a standing grant to run whatever that name later pointed at.

Only surrounding whitespace is normalized. Collapsing inner spacing or folding case would make two genuinely different command lines hash alike, and this digest is the entire basis for saying "what I approved is what will run".

The manifest is read on every request rather than cached, so a rewritten command loses its approval immediately rather than at the next daemon restart.

### The image is approved the same way

Choosing the container a command runs in *is* choosing what it can do, so the image is the machine owner's decision and is stored beside the approvals in the registry.

A project with no approved image can run nothing. That is the deliberate default: LCTK cannot know a project's toolchain, and guessing wrong would run a build in an environment that silently differs from the developer's. The refusal names the command that fixes it.

### One container per run

Each command runs in a container created and destroyed around it. This is not packaging; it is how most of the guardrails exist at all:

| Guardrail | How |
|---|---|
| Process-tree cleanup | removing the container, which happens whatever the outcome |
| PID, memory, CPU limits | the runtime's own flags |
| One mount, no others | a single `--volume` of the project root |
| No Docker socket, no other project's volume | nothing else is mounted |
| Fixed working directory | `--workdir /workspace` |
| Network policy | `--network` |
| Timeout | a deadline, then forced removal |

The project source is mounted **writable**, which is exactly why the runner is separate from the indexer: a build must write its output, and the indexer's mount is read-only and stays that way.

The command is handed to a shell, because a shell line is what a developer would type and what a manifest naturally contains. That is safe only because of everything above: nothing reaches the shell that a human has not read.

### No network unless the project asked

`none` is the default. A build that does not need the internet should not have it, and a project that does says so once. The reverse default would hand every command egress by accident.

`full` means the project's *own* Docker network rather than the default bridge, so a command with egress still cannot reach another project's services.

### A non-zero exit is a result

A failing test is the ordinary case. Reporting it as a tool error would leave a caller unable to tell it from the runtime being down, and it would make an agent treat a legitimate answer as a malfunction. A timeout is reported separately, because "the tests failed" and "the tests never finished" call for different things.

### Everything is recorded, including refusals

One append-only line per run in the LCTK home: what was asked for, what actually ran, in which image, on which network, which client asked, the exit code, and the tail of the output. A refusal is recorded too — "the agent kept asking and was kept out" is something an operator needs to be able to see.

Append-only here is not tamper resistance, which a local file cannot offer against its owner. It is the smaller property that a later entry does not disturb an earlier one.

## Alternatives considered

- **Let the client send a command line, and validate it.** Every version of this is an arms race with quoting, and the validator becomes the security boundary. A closed vocabulary has no such surface.
- **Approve a command by name only, not by text.** Simpler, and it hands a repository a standing grant to redefine what that name runs.
- **Store the approved text in the registry and run that.** Removes the tamper problem but creates two sources of truth for the same command; the manifest and the registry would disagree and nobody would know which was authoritative. Storing only the digest keeps the manifest the single statement of what the command is, and the registry the single statement of what was agreed.
- **A long-lived runner container per project.** Fewer container creations, but process-tree cleanup, resource caps, and the network policy all become things to enforce rather than things a fresh container gives for free.
- **Run commands on the host.** Uses the developer's real toolchain and needs no image. It also has none of the guardrails: no portable PID, CPU, or memory cap across Windows and macOS, no network policy, and no reliable way to kill a process tree.

## Consequences

### Positive

- A client can only ever run things a human read and approved, and cannot describe a new one.
- A repository cannot redefine an approved command; the approval lapses on any edit.
- The guardrails are the runtime's rather than LCTK's, so they hold against a command that ignores them.
- What ran is recorded, with what was refused.

### Negative

- A project needs an image chosen before it can run anything, which is a setup step with no sensible default.
- One container per run means no warm toolchain and no shared package cache: a Go or Node build re-downloads its dependencies unless the image already contains them. That is a real cost, and the honest fix is a project-supplied image that carries what its build needs.
- The three-name vocabulary does not cover projects whose useful commands are not build, test, or lint.

### Follow-up

- A cache mount would remove the re-download cost, but it is a second mount and therefore a second thing to reason about. Not added without a decision of its own.
- The timeout, PID cap, and memory cap are values rather than measurements.
- The admin page can show approvals but cannot yet grant them; approval is a CLI act.
