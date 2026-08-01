package searchindex

import (
	"context"
	"testing"
)

func TestIgnoredDirectoriesAreNotWalkedOrIndexed(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, ".gitignore", "# local evidence\n.research/\nbuild/\n*.log\n")
	f.write(t, "keep.go", "// Needle keep\n")
	f.write(t, ".research/cache/deep/thing.go", "// Needle research\n")
	f.write(t, "build/output.go", "// Needle build\n")
	f.write(t, "debug.log", "Needle log\n")
	f.write(t, "internal/nested.log", "Needle nested log\n")

	state := f.rebuild(t)
	if state.SkippedIgnored == 0 {
		t.Error("nothing was reported as skipped by ignore rules")
	}

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "keep.go" {
		t.Errorf("files = %v, want only keep.go", found)
	}
	// The inventory itself must not contain the ignored files, or a later
	// reconcile would keep rediscovering them.
	for name := range state.Files {
		if name != "keep.go" && name != ".gitignore" {
			t.Errorf("the inventory contains an ignored entry: %s", name)
		}
	}
}

func TestDefaultIgnoresApplyWithoutAnIgnoreFile(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, "node_modules/left-pad/index.js", "// Needle dependency\n")
	f.write(t, "__pycache__/thing.pyc", "Needle bytecode\n")
	f.rebuild(t)

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "app.go" {
		t.Errorf("files = %v, want only app.go", found)
	}
}

func TestAProjectCanOverruleADefaultIgnore(t *testing.T) {
	// LCTK's defaults are a guess about a project's layout; the project's own
	// rules are a statement about it. The statement has to win.
	f := newFixture(t, smallLimits)
	f.write(t, ".gitignore", "!node_modules/\n")
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, "node_modules/local/index.js", "// Needle vendored\n")
	f.rebuild(t)

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 2 {
		t.Errorf("files = %v, want the re-included dependency directory as well", found)
	}
}

func TestNestedIgnoreFilesApplyToTheirOwnSubtree(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "top.go", "// Needle top\n")
	f.write(t, "pkg/inner.go", "// Needle inner\n")
	f.write(t, "pkg/.gitignore", "generated.go\n")
	f.write(t, "pkg/generated.go", "// Needle generated\n")
	// A sibling directory must not inherit pkg's rules.
	f.write(t, "other/generated.go", "// Needle other generated\n")
	f.rebuild(t)

	found := map[string]bool{}
	for _, path := range paths(f.search(t, Request{Pattern: "Needle"})) {
		found[path] = true
	}
	if found["pkg/generated.go"] {
		t.Error("a nested rule did not apply to its own directory")
	}
	if !found["other/generated.go"] {
		t.Error("a nested rule leaked into a sibling directory")
	}
	if !found["top.go"] || !found["pkg/inner.go"] {
		t.Errorf("ordinary files were lost: %v", found)
	}
}

func TestANegationCanReIncludeAFileInAnIgnoredPattern(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, ".gitignore", "*.log\n!keep.log\n")
	f.write(t, "drop.log", "Needle drop\n")
	f.write(t, "keep.log", "Needle keep\n")
	f.rebuild(t)

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "keep.log" {
		t.Errorf("files = %v, want only keep.log", found)
	}
}

func TestAnUpdateAgreesWithARebuildAboutIgnoredFiles(t *testing.T) {
	// If a targeted update indexed an ignored file, the next rebuild would drop
	// it again and the index would appear to lose content on its own.
	f := newFixture(t, smallLimits)
	f.write(t, ".gitignore", "secrets/\n")
	f.write(t, "app.go", "// Needle app\n")
	f.rebuild(t)

	f.write(t, "secrets/token.go", "// Needle secret\n")
	if _, err := f.Update(context.Background(), []Change{{Path: "secrets/token.go"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "app.go" {
		t.Errorf("an update indexed an ignored file: %v", found)
	}

	// And a reconcile must not treat the ignored file as a pending change
	// forever.
	_, changes, err := f.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("reconcile reported %d changes for an ignored file: %+v", len(changes), changes)
	}
}

func TestVersionControlMetadataIsAlwaysSkipped(t *testing.T) {
	f := newFixture(t, smallLimits)
	// Even a project that tries to re-include it does not get it: nothing in a
	// version-control directory is source, and the objects are not text.
	f.write(t, ".gitignore", "!.git/\n")
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, ".git/config", "Needle config\n")
	f.rebuild(t)

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "app.go" {
		t.Errorf("files = %v, want only app.go", found)
	}
}

func TestIgnorePatternMatching(t *testing.T) {
	cases := []struct {
		rule    string
		subject string
		isDir   bool
		want    bool
	}{
		{rule: "node_modules/", subject: "node_modules", isDir: true, want: true},
		{rule: "node_modules/", subject: "a/b/node_modules", isDir: true, want: true},
		{rule: "node_modules/", subject: "node_modules", isDir: false, want: false},
		{rule: "/build", subject: "build", isDir: true, want: true},
		{rule: "/build", subject: "pkg/build", isDir: true, want: false},
		{rule: "*.log", subject: "a.log", want: true},
		{rule: "*.log", subject: "deep/nested/a.log", want: true},
		{rule: "*.log", subject: "a.txt", want: false},
		{rule: "docs/*.md", subject: "docs/a.md", want: true},
		{rule: "docs/*.md", subject: "docs/sub/a.md", want: false},
		{rule: "docs/**/*.md", subject: "docs/sub/a.md", want: true},
		{rule: "a?c.go", subject: "abc.go", want: true},
		{rule: "a?c.go", subject: "abbc.go", want: false},
		{rule: "[abc].go", subject: "b.go", want: true},
		{rule: "[!abc].go", subject: "b.go", want: false},
		{rule: "[!abc].go", subject: "d.go", want: true},
		{rule: "cache", subject: "x/cache/y/z.go", want: true},
	}

	for _, testCase := range cases {
		parsed, ok := parsePattern(testCase.rule, "")
		if !ok {
			t.Errorf("rule %q did not parse", testCase.rule)
			continue
		}
		set := ignoreSet{patterns: []pattern{parsed}}
		if got := set.ignored(testCase.subject, testCase.isDir); got != testCase.want {
			t.Errorf("rule %q against %q (dir=%v) = %v, want %v",
				testCase.rule, testCase.subject, testCase.isDir, got, testCase.want)
		}
	}
}

func TestCommentsAndBlankLinesAreNotRules(t *testing.T) {
	for _, line := range []string{"", "   ", "\t", "# a comment", "#"} {
		if _, ok := parsePattern(line, ""); ok {
			t.Errorf("%q was parsed as a rule", line)
		}
	}
}
