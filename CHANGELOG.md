# Changelog

All notable changes to LCTK will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases will follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Initial architecture and product documentation.
- Apache-2.0 `LICENSE`, `NOTICE`, security policy, governance, support, and pre-contribution repository policy.
- Go 1.25 foundation with a single `lctk` executable.
- `GET /health` and `/mcp` using the official MCP Go SDK v1.4.1, with temporary compatibility tool `foundation_info`.
- Native watcher event-delivery proof using `fsnotify v1.9.0`.
- Read-only Docker Desktop availability diagnostic using Moby client v0.3.0.
- Automated CLI, HTTP, MCP transport, and watcher tests.
- Hosted Windows/macOS CI and non-publishing Windows amd64 and Darwin arm64 dry-run archives plus checksums.
