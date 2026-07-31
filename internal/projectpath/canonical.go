// Package projectpath canonicalizes native host paths and derives stable local
// project identities from them.
//
// The registry, not the model and not a repository manifest, is the authority on
// where a project lives. Everything in this package exists to make that
// authority reliable: two different spellings of the same folder must produce
// the same identity, and two different folders must never collide.
package projectpath

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// ErrNotDirectory reports a path that exists but is not a directory.
var ErrNotDirectory = errors.New("path is not a directory")

// Canonical is the result of resolving one native host path.
type Canonical struct {
	// Input is the path exactly as supplied by the user, for diagnostics.
	Input string `json:"input"`
	// Display is the resolved absolute path in its real on-disk spelling. This
	// is what the user is shown and what is mounted.
	Display string `json:"display"`
	// Key is the comparison key. On case-insensitive filesystems it is
	// case-folded, so path aliases of one folder share a key. It is never shown
	// to the user and never used as a mount path.
	Key string `json:"key"`
	// Base is the final path element of Display, used to derive a readable slug.
	Base string `json:"base"`
	// CaseInsensitive records the measured behavior of the host volume, so a
	// surprising duplicate decision can be explained after the fact.
	CaseInsensitive bool `json:"case_insensitive"`
}

// Resolve canonicalizes a native host path.
//
// The path must already exist: a project cannot be registered against a folder
// that cannot be inspected, and resolving symlinks requires the target to be
// present. Resolution applies, in order, user-home expansion, absolute
// resolution, symlink and junction resolution, platform-specific long-name
// recovery, separator normalization, and comparison-key folding.
func Resolve(input string) (Canonical, error) {
	if strings.TrimSpace(input) == "" {
		return Canonical{}, errors.New("path is empty")
	}

	expanded, err := expandHome(input)
	if err != nil {
		return Canonical{}, err
	}

	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return Canonical{}, fmt.Errorf("resolve %q: %w", input, err)
	}

	// EvalSymlinks also collapses "." and ".." against the real filesystem and
	// resolves Windows junctions and macOS firmlinks such as /var to /private/var.
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Canonical{}, fmt.Errorf("resolve %q: %w", input, err)
	}

	// Recover the real on-disk spelling. On Windows this expands 8.3 short names
	// such as PROJET~1 and fixes the case of every element.
	long, err := longPath(resolved)
	if err != nil {
		return Canonical{}, fmt.Errorf("resolve %q: %w", input, err)
	}

	info, err := os.Stat(long)
	if err != nil {
		return Canonical{}, fmt.Errorf("resolve %q: %w", input, err)
	}
	if !info.IsDir() {
		return Canonical{}, fmt.Errorf("resolve %q: %w", input, ErrNotDirectory)
	}

	display := normalizeDisplay(long)
	folds := caseInsensitive(display)
	return Canonical{
		Input:           input,
		Display:         display,
		Key:             comparisonKey(display, folds),
		Base:            filepath.Base(display),
		CaseInsensitive: folds,
	}, nil
}

func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", path, err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// normalizeDisplay produces the canonical spelling shown to the user: a cleaned
// absolute path with the platform separator, an upper-case Windows drive letter,
// and no trailing separator except for a filesystem root.
func normalizeDisplay(path string) string {
	cleaned := filepath.Clean(path)
	cleaned = upperDriveLetter(cleaned)
	return cleaned
}

// upperDriveLetter uppercases a leading Windows drive letter so that c:\work and
// C:\work agree. It leaves UNC paths and POSIX paths untouched.
func upperDriveLetter(path string) string {
	if len(path) >= 2 && path[1] == ':' {
		first := rune(path[0])
		if unicode.IsLetter(first) {
			return string(unicode.ToUpper(first)) + path[1:]
		}
	}
	return path
}

// comparisonKey folds a display path into a value safe to compare for
// "same folder". Separators become forward slashes so that the key is stable
// across platforms, and case is folded only where the host filesystem ignores
// it.
func comparisonKey(display string, folds bool) string {
	key := filepath.ToSlash(display)
	if folds {
		key = strings.ToLower(key)
	}
	if len(key) > 1 {
		key = strings.TrimSuffix(key, "/")
	}
	return key
}

// idAlphabet keeps identifiers safe in a URL path segment, in a container and
// volume name, and in a filesystem path, without depending on case.
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

const (
	// slugLimit bounds the readable part so that a long folder name cannot push
	// generated container and volume names past platform limits.
	slugLimit = 24
	// digestLength is the number of encoded characters kept from the digest.
	// Forty bits of a SHA-256 digest is ample for the number of projects one
	// developer registers, and collisions are additionally impossible to
	// register because the store rejects a duplicate comparison key.
	digestLength = 8
)

// DeriveID produces the stable local project_id for a canonical path.
//
// The identity is a function of the comparison key, so every alias of one folder
// yields the same id, and it is deterministic across restarts and reinstalls
// without consulting stored state. The readable slug is a convenience for logs
// and container names; the digest carries the uniqueness.
func DeriveID(c Canonical) string {
	digest := sha256.Sum256([]byte(c.Key))
	encoded := idEncoding.EncodeToString(digest[:])[:digestLength]

	slug := slugify(c.Base)
	if slug == "" {
		return "project-" + encoded
	}
	return slug + "-" + encoded
}

// slugify reduces a folder name to lower-case ASCII alphanumerics and single
// hyphens. Non-ASCII names collapse to empty, which is why DeriveID falls back
// to a fixed prefix rather than producing an unusable identifier.
func slugify(name string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
		if b.Len() >= slugLimit {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}
