//go:build windows

package projectpath

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// shortName asks Windows for the 8.3 alias of a path, or returns an empty string
// when the volume has no 8.3 alias for it.
func shortName(t *testing.T, path string) string {
	t.Helper()

	from, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	length, err := windows.GetShortPathName(from, nil, 0)
	if err != nil || length == 0 {
		return ""
	}
	buffer := make([]uint16, length)
	written, err := windows.GetShortPathName(from, &buffer[0], length)
	if err != nil || written == 0 {
		return ""
	}
	if written > length {
		written = length
	}
	return windows.UTF16ToString(buffer[:written])
}

// TestResolveExpandsShortName covers the alias form that only Windows produces.
// Without GetLongPathName, C:\...\PROJET~1 and C:\...\ProjectWithLongName would
// canonicalize differently and register as two projects for one folder.
func TestResolveExpandsShortName(t *testing.T) {
	root := t.TempDir()
	long := filepath.Join(root, "ProjectWithAVeryLongName")
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Fatal(err)
	}

	short := shortName(t, long)
	if short == "" {
		t.Skip("no short path name is available for this path")
	}
	if short == long {
		t.Skip("8.3 name generation appears to be disabled on this volume")
	}

	viaLong, err := Resolve(long)
	if err != nil {
		t.Fatal(err)
	}
	viaShort, err := Resolve(short)
	if err != nil {
		t.Fatal(err)
	}

	if viaShort.Display != viaLong.Display {
		t.Errorf("short name was not expanded:\n  short -> %q\n  long  -> %q",
			viaShort.Display, viaLong.Display)
	}
	if viaShort.Key != viaLong.Key {
		t.Errorf("short and long names produced different keys:\n  %q\n  %q",
			viaShort.Key, viaLong.Key)
	}
	if DeriveID(viaShort) != DeriveID(viaLong) {
		t.Error("short and long names produced different project ids")
	}
	if viaShort.Base != "ProjectWithAVeryLongName" {
		t.Errorf("base = %q, want the long name", viaShort.Base)
	}
}

// TestResolveRecoversRealCaseSpelling checks that the display path carries the
// on-disk spelling rather than whatever the user typed, because the display path
// is what gets mounted and shown.
func TestResolveRecoversRealCaseSpelling(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "MixedCaseName")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, err := Resolve(filepath.Join(root, "mixedcasename"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Base != "MixedCaseName" {
		t.Errorf("base = %q, want the real spelling MixedCaseName", resolved.Base)
	}
}

func TestResolveUppercasesDriveLetter(t *testing.T) {
	resolved, err := Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Display) < 2 || resolved.Display[1] != ':' {
		t.Skipf("temp dir is not on a drive-letter path: %q", resolved.Display)
	}
	if drive := resolved.Display[0]; drive < 'A' || drive > 'Z' {
		t.Errorf("drive letter is not upper case: %q", resolved.Display)
	}
}
