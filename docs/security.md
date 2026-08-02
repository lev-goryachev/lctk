# Trust model and security boundaries

## First-release scope

LCTK is a local-first, single-user application for the user's own trusted repositories. The first release does not promise safe execution of deliberately malicious code.

Security boundaries are primarily intended to protect against:

- an erroneous tool call by an agent;
- accidental escape from the project root;
- mixing results from different projects;
- hung or excessively resource-intensive commands;
- implicit access by another local process or browser page;
- accidental granting of administrative privileges to a coding session.

## Project isolation as a correctness boundary

Mandatory rules:

1. The authoritative `project_id` comes from the route and a validated client grant.
2. `project_id`, `repository_root`, and absolute paths from model arguments are not considered authoritative.
3. A tool operates only on the root obtained from the server-side registry.
4. Project containers mount only their own project folder.
5. Indexes, memory, caches, networks, and volumes are separated by project.
6. Cross-project isolation is verified by integration and security tests.

## Paths

The host daemon canonicalizes the registered folder using host OS facilities. A manifest from Git cannot assign the host mount.

Filesystem tools must accept project-relative paths and protect against:

- `..` traversal;
- absolute paths;
- symlink/junction escape;
- platform-specific path aliases;
- a race between validating a path and opening the file.

The precise safe file-opening strategy requires a separate ADR and OS-specific tests.

As implemented in Slice 1.5, the search boundary holds these in a specific way. The project service has exactly one workspace mounted and no way to express a second, so scope is structural rather than checked. Enumeration skips symbolic links instead of following them, because following one is the ordinary way out of a read-only mount. An absolute path, a parent traversal, or a Windows-style path is refused rather than clamped, in both a path filter and a change batch: silently reinterpreting a request is worse than declining it, because the caller then believes it searched something it did not.

As implemented in Slice 3.1, the Git surface is read-only by construction: it invokes only `status`, `diff`, and `rev-parse`, never a command that writes, and it disables optional locking so a query cannot collide with a Git operation the user is running. A path argument is refused when it is absolute, escapes the repository, or begins with a dash, rather than being reinterpreted. A project registered below a repository root is scoped to its own subtree, so its endpoint cannot report a sibling's changes. Git is executed on the host, which is the same exposure the daemon already accepts by invoking `docker` from PATH.

## Runner

The runner is separated from the gateway and indexers. For the trusted-local profile, the minimum guardrails are:

- mount only one project root;
- a fixed working directory;
- timeout and cancellation;
- an output-size limit;
- process-tree cleanup;
- PID, CPU, and RAM limits;
- explicit selection of the project's `none` or `full` network policy;
- no Docker socket or other projects' volumes;
- a local audit log of the command and result without secrets.

As implemented in Slice 3.2, every one of those guardrails is a flag on a container created and destroyed around one command, which is why they hold against a command that ignores them. Verified from inside a running container: `pids.max` 512, `memory.max` 2 GiB, `cpu.max` 2 cores, `/workspace` writable and the only mount, no `/var/run/docker.sock`, and no network resolution under the `none` policy.

What may run is decided before any of that, in [ADR-0017](adr/0017-command-policy-and-the-runner.md). A repository proposes `build`, `test`, and `lint` in its manifest; the machine owner approves each one; a client runs one **by name and only by name**, with no parameter that carries a command line. An approval is bound to the exact text approved, so a command rewritten in the repository is refused until a person approves it again — otherwise every approval would be a standing grant to run whatever that name later pointed at.

The runner image is approved the same way and for the same reason: choosing the container is choosing what a command can do. A project with no approved image runs nothing.

Every run is recorded in the LCTK home, one append-only line each, including the runs that were refused.

These measures protect against mistakes and runaway processes; they do not guarantee safe execution of hostile malware.

## Client access

The localhost endpoint is protected by automatically generated credentials. The user normally does not copy or enter them manually.

A grant restricts:

- a specific client;
- one or more explicitly selected projects;
- capability profile;
- its expiration.

A grant can be revoked or replaced independently of other clients. One project's key does not open another project unless the grant policy explicitly permits it.

As implemented in Slice 1.3, a grant is issued automatically when a project is registered and is revoked when its only project is removed; a grant covering several projects loses just the removed one. Grants live in the per-user LCTK home with owner-only permissions, never in a repository. The token is stored recoverably rather than hashed, because LCTK must be able to place it into the environment of an editor it configures, and because Slice 0.4 measured that Codex refuses an inline credential and reads one from a named environment variable. Commands withhold the token unless it is explicitly revealed, and it is never written to a log.

The endpoint checks the credential before consulting the registry, so an unauthenticated caller cannot learn which projects exist. A valid credential scoped to another project receives a distinct refusal from an unknown credential, so a client can correct itself without being told what else is registered. An unauthenticated probe of any project route receives the same typed `401` whether the project exists or not, so the diagnostic surface does not become a way to enumerate projects.

Delivery of a credential to a client is decided in [ADR-0014](adr/0014-project-credential-delivery.md). The token is placed in the environment of a process LCTK starts and exists nowhere else outside the owner-only LCTK home: no generated file contains it, and LCTK makes no durable change to the machine to deliver it. A per-project variable is used rather than one shared value, because a shared value would let a client working in one project reach another, which is exactly the isolation this section states.

## Browser access

An ordinary web page does not receive access merely because it knows the `127.0.0.1` address.

An external browser origin requires an explicit flow:

1. show the origin;
2. show the requested projects and capabilities;
3. obtain user confirmation;
4. issue a restricted browser session;
5. validate the origin on subsequent requests;
6. support allow-once, persistent allow, and revocation.

The Admin UI uses a separate local session, decided in [ADR-0016](adr/0016-admin-surface-and-local-session.md). The daemon issues an exchange code at startup into the owner-only LCTK home; `lctk admin open` places it in a one-time link; the page trades it for a session cookie and the code is spent.

A browser is not a well-behaved client, so three independent defences apply and all are required: the cookie is `HttpOnly` and `SameSite=Strict`, every request must carry a `Host` header naming loopback — which is what refuses DNS rebinding, since the attacker's hostname still appears there — and every state-changing request must echo a CSRF token a cross-origin page cannot read.

No admin handler reads a project grant and no project route reads an admin session. A coding agent holding a project token cannot administer the machine, and the admin surface never serves a grant token to the page.

## Manifest trust

A tracked `.mcp-project.yaml` may contain safe declarations:

- profile;
- excludes;
- language and tooling preferences;
- index settings;
- proposed build, test, and lint commands.

The local registry stores and confirms:

- the actual host path;
- project ID binding;
- client credentials;
- grants;
- host resource limits;
- applied command and network permissions.

The repository manifest cannot mount arbitrary host directories, grant the admin capability, or provide secrets. It may propose an indexing debounce window, which the host clamps rather than adopts, because the setting costs the machine resources and host policy owns that.

Two further documents live in the owner-only LCTK home for the same reason as the registry: they are host state about a project rather than project content, and nothing inside a repository may reach them. `settings.json` holds machine-wide watcher and resource policy. One change journal per project holds project-relative paths the host observed, and no file content.

## Guarantees not claimed

The first release does not guarantee:

- hostile repository containment;
- protection from the machine owner;
- multi-user authorization;
- enterprise compliance;
- network egress allowlisting;
- the security of third-party LSP or index binaries beyond ordinary dependency hygiene.
