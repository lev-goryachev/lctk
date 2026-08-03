package searchindex

import (
	"context"
	"os"
	"path/filepath"
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
	f.update(t, Change{Path: "secrets/token.go"})

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "app.go" {
		t.Errorf("an update indexed an ignored file: %v", found)
	}

	// And a reconcile must not treat the ignored file as a pending change
	// forever.
	_, applied, err := f.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if applied.Changed != 0 {
		t.Errorf("reconcile reported %+v for an ignored file, want nothing changed", applied)
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

func TestAnUpdateCannotReadThroughASymlinkOutOfTheWorkspace(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "inside.go", "// Needle inside\n")
	f.rebuild(t)

	outsideDir := filepath.Join(filepath.Dir(f.workspace), "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.go"), []byte("// Needle secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(f.workspace, "linkdir")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	// The path contains no traversal and no absolute prefix, so validation alone
	// accepts it. Only opening through a root refuses to leave the mount, which
	// is why the guarantee lives at the read and not in a check.
	f.update(t, Change{Path: "linkdir/secret.go"})

	for _, match := range f.search(t, Request{Pattern: "Needle"}).Matches {
		if match.Path != "inside.go" {
			t.Errorf("content from outside the workspace was indexed: %+v", match)
		}
	}
}

func TestLctkIgnoreOverridesTheVersionControlFile(t *testing.T) {
	// The motivating case: a local scratch directory is deliberately uncommitted
	// and is exactly what its owner wants to search. A version-control ignore
	// file answers a different question, so it must not have the last word.
	f := newFixture(t, smallLimits)
	f.write(t, GitIgnoreFile, ".work/\n*.tmp\n")
	f.write(t, LctkIgnoreFile, "!.work/\n")
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, ".work/notes.go", "// Needle local notes\n")
	f.write(t, "scratch.tmp", "Needle temporary\n")

	state := f.rebuild(t)
	found := map[string]bool{}
	for _, path := range paths(f.search(t, Request{Pattern: "Needle"})) {
		found[path] = true
	}
	if !found[".work/notes.go"] {
		t.Error("the local directory re-included by .lctkignore was not indexed")
	}
	if found["scratch.tmp"] {
		t.Error("a rule .lctkignore did not touch stopped applying")
	}
	if !found["app.go"] {
		t.Error("an ordinary file was lost")
	}

	sources := state.IgnoreSources
	if len(sources) != 2 || sources[0] != GitIgnoreFile || sources[1] != LctkIgnoreFile {
		t.Errorf("ignore sources = %v, want the precedence chain", sources)
	}
}

func TestLctkIgnoreCanAddExclusionsOfItsOwn(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, LctkIgnoreFile, "fixtures/\n")
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, "fixtures/huge.go", "// Needle fixture\n")
	f.rebuild(t)

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "app.go" {
		t.Errorf("files = %v, want only app.go", found)
	}
}

func TestTheLocalIgnoreFileHasTheFinalWord(t *testing.T) {
	// A personal decision does not belong in a shared file, so the untracked
	// local override is applied last and can undo the committed one.
	f := newFixture(t, smallLimits)
	f.write(t, GitIgnoreFile, "notes/\n")
	f.write(t, LctkIgnoreFile, "notes/\n")
	f.write(t, LctkIgnoreLocalFile, "!notes/\napp.go\n")
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, "notes/todo.go", "// Needle notes\n")

	state := f.rebuild(t)
	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "notes/todo.go" {
		t.Errorf("files = %v, want only notes/todo.go", found)
	}
	if len(state.IgnoreSources) != 3 {
		t.Errorf("ignore sources = %v, want all three", state.IgnoreSources)
	}
}

func TestAProjectCanStartFromACleanSlate(t *testing.T) {
	// The documented escape hatch for a project whose version-control rules say
	// nothing useful about indexing.
	f := newFixture(t, smallLimits)
	f.write(t, GitIgnoreFile, "*\n")
	f.write(t, LctkIgnoreFile, "!/**\nsecrets/\n")
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, "generated/out.go", "// Needle generated\n")
	f.write(t, "secrets/token.go", "// Needle secret\n")
	f.rebuild(t)

	found := map[string]bool{}
	for _, path := range paths(f.search(t, Request{Pattern: "Needle"})) {
		found[path] = true
	}
	if !found["app.go"] || !found["generated/out.go"] {
		t.Errorf("the clean slate did not re-include everything: %v", found)
	}
	if found["secrets/token.go"] {
		t.Error("a rule after the clean slate did not apply")
	}
}

func TestNestedLctkIgnoreFilesApply(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, GitIgnoreFile, "generated.go\n")
	f.write(t, "pkg/"+LctkIgnoreFile, "!generated.go\n")
	f.write(t, "pkg/generated.go", "// Needle generated\n")
	f.write(t, "other/generated.go", "// Needle other\n")
	f.rebuild(t)

	found := map[string]bool{}
	for _, path := range paths(f.search(t, Request{Pattern: "Needle"})) {
		found[path] = true
	}
	if !found["pkg/generated.go"] {
		t.Error("a nested .lctkignore did not re-include a file its parent excluded")
	}
	if found["other/generated.go"] {
		t.Error("the nested re-inclusion leaked into a sibling directory")
	}
}

func TestNoIgnoreFilesMeansNoReportedSources(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "app.go", "// Needle app\n")
	state := f.rebuild(t)
	if len(state.IgnoreSources) != 0 {
		t.Errorf("ignore sources = %v, want none", state.IgnoreSources)
	}
}

func TestEditingAnIgnoreFileRebuildsTheWholeIndex(t *testing.T) {
	// A new rule changes what belongs in the index everywhere beneath it. A delta
	// could only act on the batch it was handed, so an already-indexed file that
	// the new rule excludes would linger.
	f := newFixture(t, smallLimits)
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, "fixtures/data.go", "// Needle fixture\n")
	f.rebuild(t)
	if got := len(paths(f.search(t, Request{Pattern: "Needle"}))); got != 2 {
		t.Fatalf("baseline files = %d, want 2", got)
	}

	f.write(t, LctkIgnoreFile, "fixtures/\n")
	state := f.update(t, Change{Path: LctkIgnoreFile})
	if !state.FullBuild {
		t.Error("an ignore-file change was applied as a delta")
	}

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "app.go" {
		t.Errorf("files = %v, want only app.go once the new rule applies", found)
	}
	if len(state.IgnoreSources) != 1 || state.IgnoreSources[0] != LctkIgnoreFile {
		t.Errorf("ignore sources = %v", state.IgnoreSources)
	}
}

func TestReconcileNoticesANewIgnoreFile(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, "fixtures/data.go", "// Needle fixture\n")
	f.rebuild(t)

	// Written while nothing was watching, which is the case reconciliation exists
	// for.
	f.write(t, LctkIgnoreFile, "fixtures/\n")
	state, _, err := f.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !state.FullBuild {
		t.Error("a new ignore file did not trigger a rebuild")
	}
	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "app.go" {
		t.Errorf("files = %v, want only app.go", found)
	}
}

func TestAnIgnoreFileIsNeverItselfIgnored(t *testing.T) {
	// A rule that hides its own edits is a rule that silently stops matching what
	// is indexed. The local override is the case that matters: projects normally
	// add it to .gitignore, and if that hid it, a change to it would go unnoticed
	// and reconciliation would never re-apply the rules.
	f := newFixture(t, smallLimits)
	f.write(t, GitIgnoreFile, ".lctkignore.local\n*.cfg\n")
	f.write(t, LctkIgnoreLocalFile, "notes/\n")
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, "notes/todo.go", "// Needle notes\n")
	f.write(t, "settings.cfg", "Needle config\n")

	state := f.rebuild(t)
	if _, present := state.Files[LctkIgnoreLocalFile]; !present {
		t.Error("the local ignore file is missing from the inventory, so a change to it would go unnoticed")
	}
	if _, present := state.Files["settings.cfg"]; present {
		t.Error("an ordinary ignored file was indexed")
	}
	if _, present := state.Files["notes/todo.go"]; present {
		t.Error("the local override did not apply")
	}
	if len(state.IgnoreSources) != 2 {
		t.Errorf("ignore sources = %v, want both files", state.IgnoreSources)
	}
}

func TestAChangeToAnIgnoredIgnoreFileStillForcesARebuild(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, GitIgnoreFile, ".lctkignore.local\n")
	f.write(t, "app.go", "// Needle app\n")
	f.write(t, "fixtures/data.go", "// Needle fixture\n")
	f.rebuild(t)

	f.write(t, LctkIgnoreLocalFile, "fixtures/\n")
	state, applied, err := f.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !state.FullBuild || !applied.Rebuilt {
		t.Errorf("a gitignored ignore file did not trigger a rebuild; applied = %+v", applied)
	}
	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 1 || found[0] != "app.go" {
		t.Errorf("files = %v, want only app.go", found)
	}
}
