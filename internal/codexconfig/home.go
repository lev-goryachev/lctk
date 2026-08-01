// Package codexconfig generates and maintains the Codex client configuration
// entry for an LCTK project endpoint.
//
// The contract this package implements is measured rather than assumed; see
// [ADR-0012] and the [measured results]. Three properties drive the design:
//
//   - Codex discovers MCP servers only through CODEX_HOME/config.toml, and an
//     inline credential is rejected, so the generated entry names an environment
//     variable and never contains a secret;
//   - that file is shared with the ChatGPT desktop application and the CLI, so
//     LCTK is not its only writer and must change only the region it owns;
//   - one malformed key aborts the entire configuration load and silently
//     removes every configured server, so a generated document is parsed before
//     it is written.
//
// [ADR-0012]: ../../docs/adr/0012-codex-integration-contract.md
// [measured results]: ../../docs/spikes/codex-compatibility-results.md
package codexconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvHome is the environment variable Codex uses to relocate its home
// directory. LCTK honors it so a caller can point every command at an isolated
// directory instead of the operator's real configuration.
const EnvHome = "CODEX_HOME"

// FileName is the configuration document inside the Codex home.
const FileName = "config.toml"

// BackupSuffix is appended to the configuration file name before LCTK writes.
// A single rolling backup is deliberate: it is predictable, it is obviously
// LCTK's, and it does not accumulate copies of a file the user owns.
const BackupSuffix = ".lctk-backup"

// Home returns the Codex home directory without creating it.
func Home() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvHome)); override != "" {
		absolute, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve %s %q: %w", EnvHome, override, err)
		}
		return absolute, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate the user home directory: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

// Path returns the Codex configuration file path without creating it.
func Path() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, FileName), nil
}
