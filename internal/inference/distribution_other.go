//go:build !windows

package inference

import "os"

// replaceSelectionFile uses the POSIX same-filesystem atomic replacement.
func replaceSelectionFile(staged, target string) error {
	return os.Rename(staged, target)
}
