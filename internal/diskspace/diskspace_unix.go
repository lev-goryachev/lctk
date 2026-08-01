//go:build !windows

package diskspace

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// available uses statfs and reports Bavail rather than Bfree: the difference is
// the reserve only a privileged process may use, which LCTK is not.
func available(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("read free space for %q: %w", path, err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
