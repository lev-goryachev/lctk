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

These measures protect against mistakes and runaway processes; they do not guarantee safe execution of hostile malware.

## Client access

The localhost endpoint is protected by automatically generated credentials. The user normally does not copy or enter them manually.

A grant restricts:

- a specific client;
- one or more explicitly selected projects;
- capability profile;
- its expiration.

A grant can be revoked or replaced independently of other clients. One project's key does not open another project unless the grant policy explicitly permits it.

## Browser access

An ordinary web page does not receive access merely because it knows the `127.0.0.1` address.

An external browser origin requires an explicit flow:

1. show the origin;
2. show the requested projects and capabilities;
3. obtain user confirmation;
4. issue a restricted browser session;
5. validate the origin on subsequent requests;
6. support allow-once, persistent allow, and revocation.

The Admin UI uses a separate local session. The precise bootstrap mechanism for this session remains an open question.

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

The repository manifest cannot mount arbitrary host directories, grant the admin capability, or provide secrets.

## Guarantees not claimed

The first release does not guarantee:

- hostile repository containment;
- protection from the machine owner;
- multi-user authorization;
- enterprise compliance;
- network egress allowlisting;
- the security of third-party LSP or index binaries beyond ordinary dependency hygiene.
