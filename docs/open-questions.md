# Open questions

This document contains only decisions that have not yet been made. Once a decision is made, the item is moved to the relevant topic document and, if necessary, to an ADR.

## Exact search backend

No persistent indexed search engine has been selected. Zoekt is a candidate, but the following must be verified:

- Docker Desktop/macOS arm64 support;
- incremental update semantics;
- a single working tree and dirty files, not only indexed remote branches;
- path/language filtering;
- resource cost and license.

## Codex configuration and credentials

The current official Codex configuration schema must be checked to answer:

- where project-local configuration can be generated safely;
- whether a bearer token can be supplied without manual environment variables;
- whether a local proxy or helper is required;
- whether secret-free configuration is committed or always remains local;
- how extension reload and reconnect detect changes.

## Daemon management API

A transport and permission model must be selected among the `lctk` CLI and Admin UI, the host daemon, and the container control plane:

- localhost HTTP;
- named pipe/Unix domain socket;
- a combined approach.

The Admin API must not accidentally become a regular project MCP capability.

## Runtime policy details

Open items:

- always-on startup semantics after OS sign-in;
- the default idle timeout `N` for on-demand mode;
- startup timeout and the behavior of a long-held MCP call;
- machine-wide settings or project overrides for individual process policies;
- precise draining and shutdown behavior during an active background operation.

## Watcher/indexing policy

Open items:

- debounce default: 3 or 5 seconds;
- debounce configuration scope: machine only, or machine default plus project override;
- bulk-change thresholds;
- which generated or excluded paths count as activity;
- how to handle project configurations that intentionally index generated code;
- the atomic consistency model across multiple backends.

## Resource planning

The following must be defined:

- a formula for forecasting disk use and a recommended cap;
- RAM and CPU budgets by profile and backend;
- when to warn, prohibit startup, or permit swapping;
- the lifetime of the shared embedding inference process;
- the meaning of `ready`: a warm process or an API capable of starting a worker quickly;
- performance classes for baseline and stress repositories.

## Language adapters

The initial ecosystems have been chosen, but their order has not been accepted:

- TypeScript/JavaScript;
- Python;
- Rust;
- C/C++.

Supported LSP implementations, toolchain versions, runner images, and rules for installing third-party binaries must be selected.

## Admin UI authentication

The principle of automatic local protection and explicit client grants has been accepted. No specific seamless login flow has been selected for the local Admin UI:

- a one-time URL from `lctk ui`;
- OS-integrated launcher;
- loopback session bootstrap;
- another mechanism without a permanent manually entered password.

## Manifest schema

It has been accepted that a safe `.mcp-project.yaml` may be stored in Git, while the host path, secrets, and grants may not. The following must be formalized:

- schema versioning;
- local override file/location;
- the agent-generated command proposal and user confirmation flow;
- environment/secret references;
- migration and backward compatibility.
