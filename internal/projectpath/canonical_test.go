package projectpath

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestResolveProducesAbsoluteCleanedDisplayPath(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "alpha", "beta")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	// A path containing ".." and a trailing separator must resolve to the same
	// canonical folder as the direct path.
	messy := filepath.Join(root, "alpha", "beta", "..", "beta") + string(filepath.Separator)

	direct, err := Resolve(nested)
	if err != nil {
		t.Fatal(err)
	}
	viaMessy, err := Resolve(messy)
	if err != nil {
		t.Fatal(err)
	}

	if direct.Display != viaMessy.Display {
		t.Errorf("display differs:\n  %q\n  %q", direct.Display, viaMessy.Display)
	}
	if direct.Key != viaMessy.Key {
		t.Errorf("key differs:\n  %q\n  %q", direct.Key, viaMessy.Key)
	}
	if DeriveID(direct) != DeriveID(viaMessy) {
		t.Error("aliases of one folder produced different project ids")
	}
	if !filepath.IsAbs(direct.Display) {
		t.Errorf("display is not absolute: %q", direct.Display)
	}
	if direct.Base != "beta" {
		t.Errorf("base = %q, want beta", direct.Base)
	}
	if direct.Input != nested {
		t.Errorf("input not preserved: %q", direct.Input)
	}
}

func TestResolveRejectsMissingEmptyAndNonDirectory(t *testing.T) {
	if _, err := Resolve(""); err == nil {
		t.Error("empty path was accepted")
	}
	if _, err := Resolve("   "); err == nil {
		t.Error("blank path was accepted")
	}
	if _, err := Resolve(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("missing path was accepted")
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(file)
	if !errors.Is(err, ErrNotDirectory) {
		t.Errorf("resolving a file: got %v, want ErrNotDirectory", err)
	}
}

func TestResolveReportsMeasuredCaseSensitivity(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "MixedCase")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS == "windows" && !resolved.CaseInsensitive {
		t.Error("Windows volume reported as case-sensitive")
	}

	// Whatever the volume does, the report must agree with what the filesystem
	// actually does for this path.
	lowered := filepath.Join(root, strings.ToLower("MixedCase"))
	_, statErr := os.Stat(lowered)
	aliasResolves := statErr == nil
	if aliasResolves != resolved.CaseInsensitive {
		t.Errorf("CaseInsensitive = %t but a lower-case alias resolving = %t",
			resolved.CaseInsensitive, aliasResolves)
	}
}

func TestResolveTreatsCaseAliasAsOneProjectWhenVolumeFoldsCase(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "CaseAlias")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.CaseInsensitive {
		t.Skip("volume is case-sensitive; a case alias is a different folder here")
	}

	alias, err := Resolve(filepath.Join(root, "casealias"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Key != alias.Key {
		t.Errorf("case alias produced a different key:\n  %q\n  %q", resolved.Key, alias.Key)
	}
	if DeriveID(resolved) != DeriveID(alias) {
		t.Error("case alias produced a different project id")
	}
}

func TestResolveFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink in this environment: %v", err)
	}

	viaTarget, err := Resolve(target)
	if err != nil {
		t.Fatal(err)
	}
	viaLink, err := Resolve(link)
	if err != nil {
		t.Fatal(err)
	}

	if viaTarget.Key != viaLink.Key {
		t.Errorf("symlink was not resolved to its target:\n  %q\n  %q", viaTarget.Key, viaLink.Key)
	}
	if DeriveID(viaTarget) != DeriveID(viaLink) {
		t.Error("a symlink and its target produced different project ids")
	}
}

func TestComparisonKeySeparatorsAndCase(t *testing.T) {
	// Keys are always derived from host-native paths, because they come from
	// Resolve. filepath.ToSlash converts only the host separator, so the
	// separator case is built with filepath.Join rather than a hard-coded
	// Windows path that would keep its backslashes on a POSIX host.
	native := filepath.Join("Work", "Repo")
	key := comparisonKey(native, true)
	if strings.ContainsRune(key, '\\') {
		t.Errorf("key %q still contains a host separator", key)
	}
	if key != "work/repo" {
		t.Errorf("key = %q, want work/repo", key)
	}

	// Case-sensitive volume: case must be preserved so distinct folders stay distinct.
	if got, want := comparisonKey("/work/Repo", false), "/work/Repo"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// A trailing separator must not change the key, but a filesystem root must
	// not be emptied.
	if comparisonKey("/work/repo/", false) != comparisonKey("/work/repo", false) {
		t.Error("trailing separator changed the key")
	}
	if got := comparisonKey("/", false); got != "/" {
		t.Errorf("root key = %q, want /", got)
	}
}

func TestUpperDriveLetter(t *testing.T) {
	cases := map[string]string{
		`c:\work`:      `C:\work`,
		`D:\work`:      `D:\work`,
		`/work/repo`:   `/work/repo`,
		`\\server\shr`: `\\server\shr`,
		``:             ``,
	}
	for in, want := range cases {
		if got := upperDriveLetter(in); got != want {
			t.Errorf("upperDriveLetter(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveIDDistinguishesDrivesAndFolders(t *testing.T) {
	// The roadmap requires that different Windows drives never collide. This is
	// checked on synthetic paths so the test does not depend on a second volume
	// existing on the runner.
	same := Canonical{Key: "c:/work/repo", Base: "repo", CaseInsensitive: true}
	otherDrive := Canonical{Key: "d:/work/repo", Base: "repo", CaseInsensitive: true}
	otherFolder := Canonical{Key: "c:/work/other", Base: "other", CaseInsensitive: true}

	if DeriveID(same) == DeriveID(otherDrive) {
		t.Error("the same relative path on two drives produced one project id")
	}
	if DeriveID(same) == DeriveID(otherFolder) {
		t.Error("two folders produced one project id")
	}
	if DeriveID(same) != DeriveID(same) {
		t.Error("DeriveID is not deterministic")
	}

	// Sibling folders whose names share the slug prefix must still differ, so the
	// digest and not the slug has to carry uniqueness.
	longA := Canonical{Key: "c:/work/a-very-long-project-name-one", Base: "a-very-long-project-name-one", CaseInsensitive: true}
	longB := Canonical{Key: "c:/work/a-very-long-project-name-two", Base: "a-very-long-project-name-two", CaseInsensitive: true}
	if DeriveID(longA) == DeriveID(longB) {
		t.Error("two folders with a shared long prefix produced one project id")
	}
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func TestDeriveIDIsSafeInRoutesAndResourceNames(t *testing.T) {
	cases := []Canonical{
		{Key: "c:/work/repo", Base: "repo"},
		{Key: "c:/work/My Project (v2)!", Base: "My Project (v2)!"},
		{Key: "/work/.hidden", Base: ".hidden"},
		{Key: "/work/проект", Base: "проект"},
		{Key: "/work/---", Base: "---"},
		{Key: "/", Base: string(filepath.Separator)},
		{Key: "c:/work/a-name-far-longer-than-the-slug-limit-should-allow", Base: "a-name-far-longer-than-the-slug-limit-should-allow"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		id := DeriveID(c)
		if !idPattern.MatchString(id) {
			t.Errorf("id %q from base %q is not safe for a route or resource name", id, c.Base)
		}
		if len(id) > slugLimit+1+digestLength {
			t.Errorf("id %q is longer than the documented bound", id)
		}
		if previous, clash := seen[id]; clash {
			t.Errorf("id %q generated for both %q and %q", id, previous, c.Key)
		}
		seen[id] = c.Key
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"repo":            "repo",
		"My Project":      "my-project",
		"UPPER_case-123":  "upper-case-123",
		"...dots...":      "dots",
		"проект":          "",
		"":                "",
		"a!!!!!!!!!!!!!b": "a-b",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slugify("проект"); got != "" {
		t.Fatalf("expected a non-ASCII name to collapse, got %q", got)
	}
	// A name that collapses must still yield a usable identifier.
	id := DeriveID(Canonical{Key: "/work/проект", Base: "проект"})
	if !strings.HasPrefix(id, "project-") {
		t.Errorf("collapsed name produced %q, want a project- prefix", id)
	}
}
