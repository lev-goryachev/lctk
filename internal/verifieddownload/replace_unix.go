//go:build !windows

package verifieddownload

import "os"

// replaceFile uses the POSIX rename replacement contract so activation is one
// filesystem operation and readers see either the old or new complete file.
func replaceFile(staged, target string) error {
	return os.Rename(staged, target)
}
