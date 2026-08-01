package codexconfig

import (
	"errors"
	"fmt"
	"strings"
)

// EntryPrefix begins every server name LCTK generates, so an entry is
// attributable at a glance in a file with other writers.
const EntryPrefix = "lctk_"

// Entry is the subset of a Codex `mcp_servers` entry that LCTK generates for a
// Streamable HTTP project endpoint.
//
// A token cannot be represented here. Codex rejects an inline bearer token for
// this transport, so the credential is always referenced by the name of an
// environment variable the Codex process reads.
type Entry struct {
	// Name is the Codex server name, produced by [EntryName].
	Name string
	// URL is the project endpoint.
	URL string
	// BearerTokenEnvVar names the variable holding the project grant token.
	BearerTokenEnvVar string
	// StartupTimeoutSec and ToolTimeoutSec are emitted only when set. Their
	// enforcement is an open measurement in ADR-0012, so LCTK does not impose a
	// default it has not observed.
	StartupTimeoutSec float64
	ToolTimeoutSec    float64
	Enabled           bool
}

// EntryName derives the Codex server name for a project.
//
// The name is not a scope authority. Per ADR-0012 the route decides scope, and
// LCTK must not depend on this name being unique or unmodified in a file it does
// not own; the name exists so a generated entry is recognizable.
func EntryName(projectID string) string {
	var b strings.Builder
	b.WriteString(EntryPrefix)
	for i := 0; i < len(projectID); i++ {
		c := projectID[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c - 'A' + 'a')
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Validate reports whether the entry can be rendered into a usable server.
func (e Entry) Validate() error {
	switch {
	case strings.TrimSpace(e.Name) == "":
		return errors.New("the entry has no name")
	case strings.TrimSpace(e.URL) == "":
		return fmt.Errorf("entry %q has no url", e.Name)
	case strings.TrimSpace(e.BearerTokenEnvVar) == "":
		// An entry without a credential reference would connect and then be
		// refused by the gateway, which reads as a broken endpoint rather than a
		// missing credential.
		return fmt.Errorf("entry %q names no bearer token variable", e.Name)
	}
	return nil
}

// Render writes the entry as TOML. Key order is fixed so a regenerated entry is
// byte-identical when nothing changed, which keeps a rewrite of the user's file
// visibly a no-op.
func (e Entry) Render() string {
	var b strings.Builder
	table := "mcp_servers." + tomlKey(e.Name)
	fmt.Fprintf(&b, "[%s]\n", table)
	fmt.Fprintf(&b, "url = %s\n", tomlString(e.URL))
	fmt.Fprintf(&b, "bearer_token_env_var = %s\n", tomlString(e.BearerTokenEnvVar))
	if e.StartupTimeoutSec > 0 {
		fmt.Fprintf(&b, "startup_timeout_sec = %s\n", trimFloat(e.StartupTimeoutSec))
	}
	if e.ToolTimeoutSec > 0 {
		fmt.Fprintf(&b, "tool_timeout_sec = %s\n", trimFloat(e.ToolTimeoutSec))
	}
	fmt.Fprintf(&b, "enabled = %t\n", e.Enabled)
	return b.String()
}

// tomlString quotes a TOML basic string. Interpolating an unescaped value is the
// specific mistake that aborts the whole configuration load, so every generated
// value goes through this.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlKey renders a table key, quoting it when it is not a bare key.
func tomlKey(s string) string {
	if s == "" {
		return `""`
	}
	for _, r := range s {
		bare := r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !bare {
			return tomlString(s)
		}
	}
	return s
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.3f", f)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
