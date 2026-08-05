//go:build windows

package verifieddownload

import "golang.org/x/sys/windows"

// replaceFile requests the Windows replace-existing and write-through
// guarantees that os.Rename intentionally does not provide on this platform.
func replaceFile(staged, target string) error {
	stagedPath, err := windows.UTF16PtrFromString(staged)
	if err != nil {
		return err
	}
	targetPath, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(stagedPath, targetPath, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
