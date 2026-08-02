package gitinfo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests below drive a real repository rather than a fixture of what Git's
// output was assumed to be. Porcelain v2 is a grammar with corners — renames
// carry a second path in a separate field, a path can hold a newline — and a
// fixture only proves the parser agrees with whoever wrote the fixture.
type repo struct{ dir string }

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	dir := t.TempDir()
	r := &repo{dir: dir}
	r.git(t, "init", "--initial-branch=main")
	r.git(t, "config", "user.email", "test@example.invalid")
	r.git(t, "config", "user.name", "Test")
	// Commit signing and hooks would make these tests depend on the machine's
	// configuration rather than on the repository they create.
	r.git(t, "config", "commit.gpgsign", "false")
	return r
}

func (r *repo) git(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = r.dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+r.dir, "USERPROFILE="+r.dir)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *repo) write(t *testing.T, relative, content string) {
	t.Helper()
	path := filepath.Join(r.dir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// status reads without the cache, so a test can make a change and see it.
func (r *repo) status(t *testing.T, options Options) Status {
	t.Helper()
	reader := New()
	reader.Now = func() time.Time { return time.Time{} }
	status, err := reader.read(context.Background(), r.dir, withDefaults(options))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return status
}

func withDefaults(options Options) Options {
	if options.MaxFiles <= 0 {
		options.MaxFiles = DefaultMaxFiles
	}
	return options
}

func find(status Status, path string) (Change, bool) {
	for _, change := range status.Changed {
		if change.Path == path {
			return change, true
		}
	}
	return Change{}, false
}

// A folder is not always a repository, and a caller asking what changed deserves
// an answer rather than a failure to interpret.
func TestAFolderThatIsNotARepositoryIsAnAnswer(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	reader := New()
	status, err := reader.read(context.Background(), t.TempDir(), withDefaults(Options{}))
	if err != nil {
		t.Fatalf("a plain folder produced an error: %v", err)
	}
	if status.Repository {
		t.Fatal("a plain folder was reported as a repository")
	}
}

// A repository with no commit is not the same as no repository.
func TestAnUnbornRepositoryIsDistinguishable(t *testing.T) {
	r := newRepo(t)
	status := r.status(t, Options{IncludeUntracked: true})

	if !status.Repository {
		t.Fatal("a freshly initialised repository was not recognised")
	}
	if !status.Unborn {
		t.Fatal("a repository with no commit was not reported as unborn")
	}
	if status.Commit != "" {
		t.Fatalf("commit = %q, want none", status.Commit)
	}
	if status.Branch != "main" {
		t.Fatalf("branch = %q, want main", status.Branch)
	}
}

func TestACleanTreeIsNotDirty(t *testing.T) {
	r := newRepo(t)
	r.write(t, "a.go", "package a\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")

	status := r.status(t, Options{IncludeUntracked: true})
	if status.Dirty {
		t.Fatalf("a committed tree is reported dirty: %+v", status.Changed)
	}
	if status.Commit == "" || len(status.ShortCommit) != 7 {
		t.Fatalf("commit = %q, short = %q", status.Commit, status.ShortCommit)
	}
	if status.Unborn {
		t.Error("a repository with a commit was reported as unborn")
	}
}

func TestEveryKindOfChangeIsReported(t *testing.T) {
	r := newRepo(t)
	r.write(t, "kept.go", "package kept\n")
	r.write(t, "edited.go", "package edited\n")
	r.write(t, "removed.go", "package removed\n")
	r.write(t, "moved.go", "package moved\n// enough content that Git sees a rename\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")

	r.write(t, "edited.go", "package edited // changed\n")
	if err := os.Remove(filepath.Join(r.dir, "removed.go")); err != nil {
		t.Fatal(err)
	}
	r.git(t, "mv", "moved.go", "renamed.go")
	r.write(t, "added.go", "package added\n")
	r.git(t, "add", "added.go")
	r.write(t, "new.go", "package new\n")

	status := r.status(t, Options{IncludeUntracked: true})
	if !status.Dirty {
		t.Fatal("a tree with changes is reported clean")
	}

	want := map[string]string{
		"edited.go":  "modified",
		"removed.go": "deleted",
		"renamed.go": "renamed",
		"added.go":   "added",
		"new.go":     "untracked",
	}
	for path, state := range want {
		change, ok := find(status, path)
		if !ok {
			t.Errorf("%s is missing from the changed list: %+v", path, status.Changed)
			continue
		}
		if change.State != state {
			t.Errorf("%s state = %q, want %q", path, change.State, state)
		}
	}
	if _, ok := find(status, "kept.go"); ok {
		t.Error("an unchanged file appears in the changed list")
	}

	// A rename carries where it came from, which is the whole reason it is a
	// rename rather than a delete plus an add.
	if change, ok := find(status, "renamed.go"); ok && change.From != "moved.go" {
		t.Errorf("rename From = %q, want moved.go", change.From)
	}
}

// Staged and working-tree are separate facts, and a file can be both. A caller
// deciding whether to commit needs to know the tree moved on after the add.
func TestAFileStagedAndThenEditedReportsBoth(t *testing.T) {
	r := newRepo(t)
	r.write(t, "a.go", "package a\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")

	r.write(t, "a.go", "package a // staged\n")
	r.git(t, "add", "a.go")
	r.write(t, "a.go", "package a // and then edited again\n")

	status := r.status(t, Options{})
	change, ok := find(status, "a.go")
	if !ok {
		t.Fatalf("the file is missing: %+v", status.Changed)
	}
	if !change.Staged || !change.WorkingTree {
		t.Fatalf("change = %+v, want both staged and working-tree", change)
	}
	if status.Total != 1 {
		t.Fatalf("total = %d, want the path counted once", status.Total)
	}
}

// The -z form exists so that a path Git would otherwise quote parses verbatim.
func TestAPathWithAwkwardCharactersParsesVerbatim(t *testing.T) {
	r := newRepo(t)
	r.write(t, "a.go", "package a\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")

	// Non-ASCII is what Git C-quotes by default, and the space is what makes the
	// space-separated form ambiguous. Both are legal file names on Windows and
	// macOS, unlike the quote or newline that would exercise the same paths.
	awkward := "café note.go"
	r.write(t, awkward, "package awkward\n")

	status := r.status(t, Options{IncludeUntracked: true})
	if _, ok := find(status, awkward); !ok {
		t.Fatalf("the path was not parsed verbatim: %+v", status.Changed)
	}
}

func TestUntrackedFilesCanBeLeftOut(t *testing.T) {
	r := newRepo(t)
	r.write(t, "a.go", "package a\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")
	r.write(t, "new.go", "package new\n")

	if status := r.status(t, Options{IncludeUntracked: false}); status.Dirty {
		t.Fatalf("an untracked file counted as dirty when excluded: %+v", status.Changed)
	}
	if status := r.status(t, Options{IncludeUntracked: true}); !status.Dirty {
		t.Fatal("an untracked file did not count as dirty when included")
	}
}

// A trimmed list must still say how much there was, or a caller reads "12
// changes" when there were four hundred.
func TestTruncationStillReportsTheTotal(t *testing.T) {
	r := newRepo(t)
	r.write(t, "a.go", "package a\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")
	for i := 0; i < 12; i++ {
		r.write(t, "new"+strconv.Itoa(i)+".go", "package new\n")
	}

	status := r.status(t, Options{IncludeUntracked: true, MaxFiles: 5})
	if len(status.Changed) != 5 {
		t.Fatalf("kept %d changes, want 5", len(status.Changed))
	}
	if !status.Truncated {
		t.Error("a trimmed list was not reported as truncated")
	}
	if status.Total != 12 {
		t.Fatalf("total = %d, want 12", status.Total)
	}
}

func TestDetachedHeadIsReportedAsSuch(t *testing.T) {
	r := newRepo(t)
	r.write(t, "a.go", "package a\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")
	head := strings.TrimSpace(r.git(t, "rev-parse", "HEAD"))
	r.git(t, "checkout", "--detach", head)

	status := r.status(t, Options{})
	if !status.Detached {
		t.Fatal("a detached HEAD was not reported")
	}
	if status.Branch != "" {
		t.Fatalf("branch = %q, want none while detached", status.Branch)
	}
	if status.Commit != head {
		t.Fatalf("commit = %q, want %q", status.Commit, head)
	}
}

func TestDiffShowsTheChangeAndIsBounded(t *testing.T) {
	r := newRepo(t)
	r.write(t, "a.go", "package a\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")
	r.write(t, "a.go", "package a // changed\n")

	reader := New()
	diff, err := reader.Diff(context.Background(), r.dir, DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff.Patch, "// changed") {
		t.Fatalf("the diff does not contain the change:\n%s", diff.Patch)
	}
	if diff.Truncated {
		t.Error("a one-line diff was reported as truncated")
	}

	bounded, err := reader.Diff(context.Background(), r.dir, DiffOptions{MaxBytes: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !bounded.Truncated || len(bounded.Patch) != 20 {
		t.Fatalf("bounded diff = %d bytes, truncated %v", len(bounded.Patch), bounded.Truncated)
	}
}

func TestStagedDiffIsSeparateFromTheWorkingTree(t *testing.T) {
	r := newRepo(t)
	r.write(t, "a.go", "package a\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")
	r.write(t, "a.go", "package a // staged\n")
	r.git(t, "add", "a.go")

	reader := New()
	staged, err := reader.Diff(context.Background(), r.dir, DiffOptions{Staged: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged.Patch, "// staged") {
		t.Fatalf("the staged diff is missing the staged change:\n%s", staged.Patch)
	}

	working, err := reader.Diff(context.Background(), r.dir, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(working.Patch) != "" {
		t.Fatalf("the working-tree diff shows a staged-only change:\n%s", working.Patch)
	}
}

// A path argument is how a caller would try to reach outside the repository, and
// a leading dash is how one would try to pass an option where a path is expected.
func TestEscapingAndOptionLikePathsAreRefused(t *testing.T) {
	refused := []string{"", "/etc/passwd", `C:\Windows\System32`, "../outside", "a/../../b", "--output=/tmp/x"}
	for _, path := range refused {
		if _, err := cleanPaths([]string{path}); err == nil {
			t.Errorf("cleanPaths(%q) was accepted", path)
		}
	}
	accepted := []string{"internal/gitinfo/parse.go", "a.go", "docs/adr"}
	if _, err := cleanPaths(accepted); err != nil {
		t.Errorf("ordinary paths were refused: %v", err)
	}
}

// The cache exists so the freshness contract does not cost a subprocess per
// search. It has to expire, or a change would stay invisible.
func TestTheCacheServesRepeatsAndThenExpires(t *testing.T) {
	// Reads are counted by the status invocation rather than by every subprocess,
	// because one read also asks Git where the project sits in the repository.
	reads := 0
	now := time.Unix(1700000000, 0)
	reader := &Reader{
		Now: func() time.Time { return now },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			for _, arg := range args {
				if arg == "status" {
					reads++
				}
			}
			return []byte("# branch.oid abcdef1234567890\x00# branch.head main\x00"), nil
		},
		cached: map[string]entry{},
	}

	for i := 0; i < 5; i++ {
		if _, err := reader.Status(context.Background(), "/project", Options{}); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("git status ran %d times for five calls in the same instant", reads)
	}

	now = now.Add(CacheTTL + time.Millisecond)
	if _, err := reader.Status(context.Background(), "/project", Options{}); err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("git status ran %d times, want the cache to have expired", reads)
	}
}

// Two projects must not share an answer.
func TestTheCacheIsPerProject(t *testing.T) {
	seen := map[string]int{}
	reader := &Reader{
		Now: time.Now,
		Run: func(_ context.Context, dir string, _ ...string) ([]byte, error) {
			seen[dir]++
			return []byte("# branch.head " + filepath.Base(dir) + "\x00"), nil
		},
		cached: map[string]entry{},
	}

	alpha, err := reader.Status(context.Background(), "/work/alpha", Options{})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := reader.Status(context.Background(), "/work/beta", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if alpha.Branch == beta.Branch {
		t.Fatalf("two projects were served the same answer: %q", alpha.Branch)
	}
}

// Git declining to read a repository it distrusts is a fixable problem, not the
// same thing as the folder not being a repository.
func TestDubiousOwnershipIsNotMistakenForAPlainFolder(t *testing.T) {
	if notARepository(&commandError{stderr: "fatal: detected dubious ownership in repository at '/work'"}) {
		t.Fatal("a repository Git refused to read was reported as not a repository")
	}
	if !notARepository(&commandError{stderr: "fatal: not a git repository (or any of the parent directories): .git"}) {
		t.Fatal("a plain folder was not recognised")
	}
}

// A registered project can sit below a repository root. Without an explicit
// pathspec, Git would report a sibling directory's changes on that project's
// endpoint, which is the scope boundary ADR-0001 exists to hold.
func TestAProjectBelowARepositoryRootSeesOnlyItsOwnChanges(t *testing.T) {
	r := newRepo(t)
	r.write(t, "app/a.go", "package app\n")
	r.write(t, "other/b.go", "package other\n")
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "first")

	r.write(t, "app/a.go", "package app // mine\n")
	r.write(t, "other/b.go", "package other // not mine\n")

	reader := New()
	status, err := reader.read(context.Background(), filepath.Join(r.dir, "app"), withDefaults(Options{}))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if _, ok := find(status, "other/b.go"); ok {
		t.Fatalf("a sibling directory's change reached the project: %+v", status.Changed)
	}
	if _, ok := find(status, "app/a.go"); !ok {
		t.Fatalf("the project's own change is missing: %+v", status.Changed)
	}
	// Paths stay repository-relative, so the prefix is what relates them to the
	// project.
	if status.Prefix != "app/" {
		t.Fatalf("prefix = %q, want app/", status.Prefix)
	}
}
