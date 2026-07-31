//go:build !windows

package projectpath

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// caseInsensitive reports whether the volume holding path compares names without
// regard to case.
//
// It is measured rather than assumed. The default macOS volume is
// case-insensitive and case-preserving, macOS also supports case-sensitive
// volumes, and Linux is normally case-sensitive. Guessing in either direction is
// harmful: folding on a case-sensitive volume would give two genuinely different
// folders the same project identity, and not folding on a case-insensitive one
// would register one folder twice.
//
// The probe is read-only. It flips the case of the final path element and asks
// whether that name reaches the same inode.
func caseInsensitive(path string) bool {
	dir, base := filepath.Split(path)
	flipped := flipCase(base)
	if flipped == base {
		// Nothing to flip, so the volume cannot be probed this way. Report
		// case-sensitive, which keeps distinct folders distinct; registration
		// still uses os.SameFile as the authority on duplicates.
		return false
	}

	original, err := os.Stat(path)
	if err != nil {
		return false
	}
	alias, err := os.Stat(filepath.Join(dir, flipped))
	if err != nil {
		return false
	}
	return os.SameFile(original, alias)
}

// flipCase inverts the case of every cased letter, which is enough to detect
// case folding without depending on a particular letter appearing.
func flipCase(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case unicode.IsUpper(r):
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsLower(r):
			b.WriteRune(unicode.ToUpper(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// longPath returns the path unchanged. There is no short-name mechanism to undo
// outside Windows, and filepath.EvalSymlinks has already resolved symlinks and
// firmlinks such as /var to /private/var.
func longPath(path string) (string, error) {
	return path, nil
}
