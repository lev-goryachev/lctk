# Security Policy

## Supported versions

LCTK is pre-alpha and has no supported release line yet. Security fixes are made on the default branch until a release support policy is published.

## Reporting a vulnerability

Use GitHub Private Vulnerability Reporting for this repository. Do not disclose suspected vulnerabilities in public issues, discussions, pull requests, or chat logs.

Include, when possible:

- the affected commit or version;
- reproduction steps;
- expected and observed behavior;
- impact, including project-isolation or host-write consequences;
- relevant logs with secrets and private source content removed.

Receipt and remediation timelines are not guaranteed during pre-alpha. A maintainer will acknowledge actionable reports through the private GitHub advisory channel.

## Scope

The primary security and correctness boundaries are documented in [`docs/security.md`](docs/security.md). LCTK does not claim hostile multi-tenant isolation or safe execution of untrusted code.
