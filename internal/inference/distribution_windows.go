//go:build windows

package inference

import "golang.org/x/sys/windows"

// replaceSelectionFile uses replace-existing and write-through guarantees so a
// setup interruption leaves either the preceding or the complete new choice.
func replaceSelectionFile(staged, target string) error {
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
