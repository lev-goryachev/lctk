package codexconfig

import (
	"errors"
	"fmt"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Placement describes how a project's entry currently appears in a Codex
// configuration document.
type Placement string

const (
	// PlacementAbsent means no entry with this name exists.
	PlacementAbsent Placement = "absent"
	// PlacementManaged means the entry sits inside an LCTK marker region.
	PlacementManaged Placement = "managed"
	// PlacementForeign means a table with this name exists that LCTK did not
	// generate.
	PlacementForeign Placement = "foreign"
	// PlacementForeignInline means the name appears as a key inside an
	// `[mcp_servers]` table rather than as its own table.
	PlacementForeignInline Placement = "foreign_inline"
)

// Errors a caller is expected to distinguish and explain.
var (
	// ErrForeignEntry reports a same-named entry LCTK did not generate.
	ErrForeignEntry = errors.New("the configuration already defines this server outside the LCTK region")
	// ErrForeignInlineEntry reports a same-named entry written as a key rather
	// than a table. LCTK refuses to rewrite it in any mode, because replacing a
	// hand-written value in a shared file is not a change LCTK should make on
	// the user's behalf.
	ErrForeignInlineEntry = errors.New("the configuration defines this server as an inline key, which LCTK will not rewrite")
	// ErrInvalidDocument reports that a merge would produce a document Codex
	// cannot load.
	ErrInvalidDocument = errors.New("the resulting configuration is not valid TOML")
	// ErrExistingInvalid reports that the file on disk is already unparseable.
	ErrExistingInvalid = errors.New("the existing configuration is not valid TOML")
)

func beginMarker(projectID string) string { return "# lctk:begin " + projectID }
func endMarker(projectID string) string   { return "# lctk:end " + projectID }

// Location is where a project's entry was found in a document.
type Location struct {
	Placement Placement
	// First and Last are inclusive zero-based line indices of the region the
	// entry occupies. They are meaningful only when Placement is not absent.
	First int
	Last  int
}

// Locate reports how a project's entry appears in a document.
//
// A managed region wins over a same-named table, because a table inside the
// markers is the region LCTK wrote.
func Locate(document, projectID string) Location {
	lines := splitLines(document)
	name := EntryName(projectID)

	if first, last, ok := findManagedRegion(lines, projectID); ok {
		return Location{Placement: PlacementManaged, First: first, Last: last}
	}
	if first, last, ok := findServerTable(lines, name); ok {
		return Location{Placement: PlacementForeign, First: first, Last: last}
	}
	if index, ok := findInlineServerKey(lines, name); ok {
		return Location{Placement: PlacementForeignInline, First: index, Last: index}
	}
	return Location{Placement: PlacementAbsent}
}

// Merge returns the document with the project's entry present in an LCTK region.
//
// Only the region LCTK owns changes; every other byte of the caller's document
// is preserved, because that document is shared with other writers.
func Merge(document, projectID string, entry Entry, force bool) (string, error) {
	if err := entry.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(document) != "" {
		if err := toml.Unmarshal([]byte(document), &map[string]any{}); err != nil {
			return "", fmt.Errorf("%w: %v", ErrExistingInvalid, err)
		}
	}

	lines := splitLines(document)
	block := splitLines(renderRegion(projectID, entry))
	location := Locate(document, projectID)

	var merged []string
	switch location.Placement {
	case PlacementManaged:
		merged = replaceLines(lines, location.First, location.Last, block)
	case PlacementForeign:
		if !force {
			return "", fmt.Errorf("%w: %s", ErrForeignEntry, EntryName(projectID))
		}
		merged = replaceLines(lines, location.First, location.Last, block)
	case PlacementForeignInline:
		return "", fmt.Errorf("%w: %s", ErrForeignInlineEntry, EntryName(projectID))
	default:
		merged = appendBlock(lines, block)
	}

	result := joinLines(merged)
	if err := toml.Unmarshal([]byte(result), &map[string]any{}); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	return result, nil
}

// Remove returns the document without the project's LCTK region, and reports
// whether anything was removed.
//
// A same-named entry outside the region is left alone: LCTK did not write it, so
// removing an LCTK project is not a licence to delete it.
func Remove(document, projectID string) (string, bool, error) {
	location := Locate(document, projectID)
	if location.Placement != PlacementManaged {
		return document, false, nil
	}

	lines := splitLines(document)
	merged := replaceLines(lines, location.First, location.Last, nil)
	result := joinLines(trimTrailingBlanks(merged))
	if strings.TrimSpace(result) != "" {
		if err := toml.Unmarshal([]byte(result), &map[string]any{}); err != nil {
			return "", false, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
		}
	}
	return result, true, nil
}

// renderRegion wraps an entry in the markers that make it LCTK's to rewrite.
func renderRegion(projectID string, entry Entry) string {
	var b strings.Builder
	b.WriteString(beginMarker(projectID))
	b.WriteString("\n")
	b.WriteString("# Generated by LCTK. This region is rewritten; edit it through lctk.\n")
	b.WriteString("# The grant token is read from the named environment variable and is never stored here.\n")
	b.WriteString(entry.Render())
	b.WriteString(endMarker(projectID))
	b.WriteString("\n")
	return b.String()
}

func findManagedRegion(lines []string, projectID string) (int, int, bool) {
	begin, end := beginMarker(projectID), endMarker(projectID)
	first := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case first < 0 && trimmed == begin:
			first = i
		case first >= 0 && trimmed == end:
			return first, i, true
		}
	}
	return 0, 0, false
}

// findServerTable returns the inclusive line range of `[mcp_servers.<name>]`
// together with its sub-tables, which belong to the same server.
func findServerTable(lines []string, name string) (int, int, bool) {
	first := -1
	for i, line := range lines {
		path, ok := parseTableHeader(line)
		if !ok {
			continue
		}
		if first < 0 {
			if len(path) == 2 && path[0] == "mcp_servers" && path[1] == name {
				first = i
			}
			continue
		}
		if len(path) > 2 && path[0] == "mcp_servers" && path[1] == name {
			continue
		}
		return first, i - 1, true
	}
	if first < 0 {
		return 0, 0, false
	}
	return first, len(lines) - 1, true
}

// findInlineServerKey finds `<name> = ...` directly under an `[mcp_servers]`
// table.
func findInlineServerKey(lines []string, name string) (int, bool) {
	inServers := false
	for i, line := range lines {
		if path, ok := parseTableHeader(line); ok {
			inServers = len(path) == 1 && path[0] == "mcp_servers"
			continue
		}
		if !inServers {
			continue
		}
		key, ok := parseKeyName(line)
		if ok && key == name {
			return i, true
		}
	}
	return 0, false
}

// parseTableHeader returns the dotted key path of a `[table]` line.
func parseTableHeader(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
		return nil, false
	}
	end := strings.LastIndex(trimmed, "]")
	if end <= 0 {
		return nil, false
	}
	path, ok := splitKeyPath(trimmed[1:end])
	if !ok || len(path) == 0 {
		return nil, false
	}
	return path, true
}

// parseKeyName returns the single key name a `key = value` line assigns to. A
// dotted key is reported by its first segment, which is what decides whether the
// line belongs to a server.
func parseKeyName(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	equals := strings.Index(trimmed, "=")
	if equals <= 0 {
		return "", false
	}
	path, ok := splitKeyPath(trimmed[:equals])
	if !ok || len(path) == 0 {
		return "", false
	}
	return path[0], true
}

// splitKeyPath splits a dotted TOML key into its segments, unquoting basic and
// literal strings. It reports false on a malformed key rather than guessing.
func splitKeyPath(raw string) ([]string, bool) {
	var (
		segments []string
		current  strings.Builder
		quote    rune
		escaped  bool
		quoted   bool
	)
	flush := func() bool {
		text := current.String()
		current.Reset()
		if !quoted {
			text = strings.TrimSpace(text)
			if text == "" {
				return false
			}
		}
		quoted = false
		segments = append(segments, text)
		return true
	}

	for _, r := range raw {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case quote == '"' && r == '\\':
			escaped = true
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			current.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
			quoted = true
		case r == '.':
			if !flush() {
				return nil, false
			}
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	if !flush() {
		return nil, false
	}
	return segments, true
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	normalized := strings.ReplaceAll(s, "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	return strings.Split(normalized, "\n")
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func replaceLines(lines []string, first, last int, block []string) []string {
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:first]...)
	out = append(out, block...)
	if last+1 < len(lines) {
		out = append(out, lines[last+1:]...)
	}
	return out
}

// appendBlock places a new region at the end, separated by exactly one blank
// line so the file stays readable however it was formatted.
func appendBlock(lines, block []string) []string {
	out := trimTrailingBlanks(lines)
	if len(out) > 0 {
		out = append(out, "")
	}
	return append(out, block...)
}

func trimTrailingBlanks(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[:end]
}
