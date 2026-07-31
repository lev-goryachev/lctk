//go:build windows

package projectpath

import (
	"golang.org/x/sys/windows"
)

// caseInsensitive reports that the path is on a Windows volume, where names are
// compared without regard to case.
//
// Windows can enable per-directory case sensitivity for WSL interoperability,
// but the Win32 path surface that LCTK and Docker Desktop use continues to treat
// names case-insensitively, so folding is correct for this purpose.
func caseInsensitive(string) bool { return true }

// longPath expands an 8.3 short name such as C:\PROJET~1 to its real spelling
// and corrects the case of every element.
//
// filepath.EvalSymlinks resolves junctions but preserves whatever spelling the
// caller supplied, so two aliases of one folder would otherwise produce two
// different comparison keys and register as two projects.
func longPath(path string) (string, error) {
	from, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}

	// Ask for the required length first: a short name can expand considerably.
	length, err := windows.GetLongPathName(from, nil, 0)
	if err != nil {
		// The path exists, since it was already resolved, but the volume may not
		// support long-name lookup. Fall back to the resolved spelling rather
		// than failing registration.
		return path, nil
	}
	if length == 0 {
		return path, nil
	}

	buffer := make([]uint16, length)
	written, err := windows.GetLongPathName(from, &buffer[0], length)
	if err != nil || written == 0 {
		return path, nil
	}
	if written > length {
		written = length
	}
	return windows.UTF16ToString(buffer[:written]), nil
}
