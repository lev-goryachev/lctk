package searchindex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFailure(t *testing.T, f *fixture, relative string, maxBytes int64) *Error {
	t.Helper()
	_, _, err := f.ReadProjectFile(relative, maxBytes)
	if err == nil {
		t.Fatalf("ReadProjectFile(%q) succeeded, want a refusal", relative)
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error is not typed: %v", err)
	}
	return typed
}

func TestAProjectFileIsReadWithItsDigest(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "internal/app.go", "package app\n")

	content, digest, err := f.ReadProjectFile("internal/app.go", 0)
	if err != nil {
		t.Fatalf("ReadProjectFile: %v", err)
	}
	if string(content) != "package app\n" {
		t.Errorf("content = %q", content)
	}
	// The digest is what lets a caller tell whether two answers describe the same
	// bytes, so it has to be the content's own digest and not some other value.
	if digest != digestOf(content) {
		t.Errorf("digest = %q", digest)
	}
}

// A path that leaves the project is refused rather than reinterpreted. The refusal
// is a distinct code from "no such file" because the caller can fix its request.
func TestAnEscapingPathIsRefusedRatherThanClamped(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "app.go", "package app\n")

	for _, name := range []string{
		"../outside.go",
		"/etc/passwd",
		"C:\\Windows\\win.ini",
		`internal\app.go`,
		"",
		"..",
	} {
		t.Run(name, func(t *testing.T) {
			if code := readFailure(t, f, name, 0).Code; code != CodeInvalidPath {
				t.Errorf("code = %q, want %q", code, CodeInvalidPath)
			}
		})
	}
}

// The project's own ignore rules decide what may be read, because the store is
// already the single authority on what belongs to the project. A second component
// answering that question would be a second answer, and the one that drifted would
// be handing out files the project excluded.
func TestAFileTheProjectIgnoresCannotBeRead(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, ".gitignore", "secrets/\n")
	f.write(t, "secrets/token.go", "package secrets\n")
	f.write(t, "app.go", "package app\n")

	if _, _, err := f.ReadProjectFile("app.go", 0); err != nil {
		t.Fatalf("an ordinary file was refused: %v", err)
	}

	typed := readFailure(t, f, "secrets/token.go", 0)
	// The same answer as a file that is not there. Distinguishing the two would let
	// a caller map what exists outside its scope by reading the difference between
	// two refusals.
	if typed.Code != CodeFileNotFound {
		t.Errorf("code = %q, want %q", typed.Code, CodeFileNotFound)
	}
	absent := readFailure(t, f, "secrets/absent.go", 0)
	if absent.Code != typed.Code {
		t.Errorf("an ignored file answers %q and an absent one %q; the two must not be distinguishable",
			typed.Code, absent.Code)
	}
}

func TestVersionControlMetadataCannotBeRead(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, ".git/config", "[core]\n")

	if code := readFailure(t, f, ".git/config", 0).Code; code != CodeFileNotFound {
		t.Errorf("code = %q, want %q", code, CodeFileNotFound)
	}
}

// A symbolic link is the ordinary way out of a read-only mount, so it is refused
// rather than followed.
func TestASymbolicLinkIsRefusedRatherThanFollowed(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "app.go", "package app\n")

	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.workspace, "link.go")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symbolic links are unavailable here: %v", err)
	}

	if code := readFailure(t, f, "link.go", 0).Code; code != CodeFileNotFound {
		t.Errorf("code = %q, want %q", code, CodeFileNotFound)
	}
}

func TestADirectoryIsNotAFile(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "internal/app.go", "package app\n")

	if code := readFailure(t, f, "internal", 0).Code; code != CodeFileNotFound {
		t.Errorf("code = %q, want %q", code, CodeFileNotFound)
	}
}

// The size limit is the caller's, passed per call, because reading a file for one
// purpose and indexing it are bounded for different reasons.
func TestAFileAboveTheCallersLimitIsRefusedWithItsOwnCode(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "big.go", "package big\n"+strings.Repeat("// pad\n", 100))

	typed := readFailure(t, f, "big.go", 64)
	if typed.Code != CodeFileTooLarge {
		t.Errorf("code = %q, want %q", typed.Code, CodeFileTooLarge)
	}
	// The same file with no limit is readable, which is what makes the limit the
	// caller's decision rather than a property of the file.
	if _, _, err := f.ReadProjectFile("big.go", 0); err != nil {
		t.Errorf("the same file was refused with no limit: %v", err)
	}
}
