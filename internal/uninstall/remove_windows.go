//go:build windows

package uninstall

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/windows"
)

func scheduleRemoval(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	var paths []string
	if err := filepath.WalkDir(path, func(current string, _ fs.DirEntry, err error) error {
		if err == nil {
			paths = append(paths, current)
		}
		return err
	}); err != nil {
		return err
	}
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, current := range paths {
		wide, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		if err := windows.MoveFileEx(wide, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); err != nil {
			return err
		}
	}
	return nil
}
