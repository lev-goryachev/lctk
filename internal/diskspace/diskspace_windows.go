package diskspace

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// available uses GetDiskFreeSpaceEx, which reports the free bytes available to
// the calling user rather than the volume total. On a machine with disk quotas
// those differ, and the caller's share is the one that matters.
func available(path string) (uint64, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("resolve %q: %w", path, err)
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(wide, &free, &total, &totalFree); err != nil {
		return 0, fmt.Errorf("read free space for %q: %w", path, err)
	}
	return free, nil
}
