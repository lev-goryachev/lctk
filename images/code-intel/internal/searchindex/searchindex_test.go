package searchindex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// smallLimits keep the policy the same shape as production while making the
// thresholds cheap to reach in a test.
var smallLimits = Limits{MaxFileBytes: 4096, MaxDeltaGenerations: 3, KeepGenerations: 2}

type fixture struct {
	*Store
	workspace string
	root      string
}

func newFixture(t *testing.T, limits Limits) *fixture {
	t.Helper()
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	root := filepath.Join(base, "state", "index")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store := New(workspace, root, "test-project", limits)
	t.Cleanup(store.Close)
	return &fixture{Store: store, workspace: workspace, root: root}
}

func (f *fixture) write(t *testing.T, relative, content string) {
	t.Helper()
	path := filepath.Join(f.workspace, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) remove(t *testing.T, relative string) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.workspace, filepath.FromSlash(relative))); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) rebuild(t *testing.T) State {
	t.Helper()
	state, err := f.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	return state
}

// update applies a batch and returns the published state.
func (f *fixture) update(t *testing.T, changes ...Change) State {
	t.Helper()
	state, _, err := f.Update(context.Background(), changes)
	if err != nil {
		t.Fatalf("Update(%+v): %v", changes, err)
	}
	return state
}

// report applies a batch and returns what the store says it did, which is what a
// test about no-op writes has to assert on: the state alone cannot distinguish
// "nothing changed" from "nothing was submitted".
func (f *fixture) report(t *testing.T, changes ...Change) (State, Applied) {
	t.Helper()
	state, applied, err := f.Update(context.Background(), changes)
	if err != nil {
		t.Fatalf("Update(%+v): %v", changes, err)
	}
	return state, applied
}

func (f *fixture) search(t *testing.T, request Request) Response {
	t.Helper()
	response, err := f.Search(context.Background(), request)
	if err != nil {
		t.Fatalf("Search(%+v): %v", request, err)
	}
	return response
}

func paths(response Response) []string {
	seen := map[string]bool{}
	var out []string
	for _, match := range response.Matches {
		if !seen[match.Path] {
			seen[match.Path] = true
			out = append(out, match.Path)
		}
	}
	return out
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error is not typed: %v", err)
	}
	return typed.Code
}

func seed(t *testing.T, f *fixture) {
	t.Helper()
	f.write(t, "alpha.go", "package alpha\n\nfunc Needle() string { return \"found\" }\n")
	f.write(t, "internal/beta.go", "package internal\n\n// Needle appears here too.\nvar Needle = 1\n")
	f.write(t, "docs/readme.md", "# Docs\n\nNothing to see.\n")
	f.write(t, "node_modules/dep/index.js", "const Needle = 'excluded';\n")
}

func TestSearchBeforeAnyBuildIsTypedAndRetryable(t *testing.T) {
	f := newFixture(t, smallLimits)
	_, err := f.Search(context.Background(), Request{Pattern: "anything"})
	if got := codeOf(t, err); got != CodeIndexNotReady {
		t.Fatalf("code = %q, want %q", got, CodeIndexNotReady)
	}
	var typed *Error
	errors.As(err, &typed)
	if !typed.Retryable {
		t.Error("an index that has not been built yet must be reported as retryable")
	}
}

func TestRebuildIndexesTheSavedWorkingTree(t *testing.T) {
	f := newFixture(t, smallLimits)
	seed(t, f)

	state := f.rebuild(t)
	if state.Generation != 1 {
		t.Errorf("generation = %d, want 1", state.Generation)
	}
	if !state.FullBuild {
		t.Error("a rebuild must report itself as a full build")
	}

	response := f.search(t, Request{Pattern: "Needle"})
	found := paths(response)
	want := map[string]bool{"alpha.go": true, "internal/beta.go": true}
	for _, path := range found {
		if !want[path] {
			t.Errorf("unexpected file in results: %s", path)
		}
	}
	if len(found) != len(want) {
		t.Errorf("files = %v, want %v", found, want)
	}
	// Derived and vendored directories are excluded, so a match inside one must
	// not appear at all.
	for _, path := range found {
		if strings.HasPrefix(path, "node_modules/") {
			t.Errorf("an excluded directory was indexed: %s", path)
		}
	}
	if response.Generation != 1 || response.FileCount != state.FileCount {
		t.Errorf("provenance = generation %d, files %d", response.Generation, response.FileCount)
	}
}

func TestResultsComeOnlyFromTheWorkspace(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "inside.go", "// Needle inside\n")

	// A file next to the workspace, and a symlink pointing at it. Neither may
	// contribute a result: the read-only mount is the boundary, and following a
	// link out of it is the classic way to leave one.
	outsideDir := filepath.Join(filepath.Dir(f.workspace), "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(outsideDir, "secret.go")
	if err := os.WriteFile(outside, []byte("// Needle outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.workspace, "link.go")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(f.workspace, "linkdir")); err != nil {
		t.Fatal(err)
	}

	f.rebuild(t)
	response := f.search(t, Request{Pattern: "Needle"})
	for _, match := range response.Matches {
		if match.Path != "inside.go" {
			t.Errorf("a result came from outside the workspace: %+v", match)
		}
		if strings.Contains(match.Preview, "outside") {
			t.Errorf("content from outside the workspace leaked: %q", match.Preview)
		}
	}
	if len(response.Matches) == 0 {
		t.Error("the file inside the workspace was not found")
	}
}

func TestUpdateHandlesCreateModifyDeleteAndRename(t *testing.T) {
	f := newFixture(t, Limits{MaxFileBytes: 4096, MaxDeltaGenerations: 50, KeepGenerations: 3})
	f.write(t, "one.go", "// Needle one\n")
	f.write(t, "two.go", "// unrelated\n")
	f.rebuild(t)

	// Create.
	f.write(t, "three.go", "// Needle three\n")
	f.update(t, Change{Path: "three.go"})
	if got := paths(f.search(t, Request{Pattern: "Needle"})); len(got) != 2 {
		t.Errorf("after create, files = %v, want one.go and three.go", got)
	}

	// Modify: the old content must stop matching and the new content must start.
	f.write(t, "two.go", "// Needle two now\n")
	f.update(t, Change{Path: "two.go"})
	if got := len(paths(f.search(t, Request{Pattern: "Needle"}))); got != 3 {
		t.Errorf("after modify, matching files = %d, want 3", got)
	}
	if got := f.search(t, Request{Pattern: "unrelated"}); len(got.Matches) != 0 {
		t.Errorf("the replaced content still matches: %+v", got.Matches)
	}

	// Delete.
	f.remove(t, "one.go")
	f.update(t, Change{Path: "one.go", Deleted: true})
	for _, path := range paths(f.search(t, Request{Pattern: "Needle"})) {
		if path == "one.go" {
			t.Error("a deleted file still appears in results")
		}
	}

	// Rename is a delete plus a create in one batch, which is how a watcher
	// reports it.
	f.remove(t, "three.go")
	f.write(t, "renamed/four.go", "// Needle three\n")
	f.update(t,
		Change{Path: "three.go", Deleted: true},
		Change{Path: "renamed/four.go"},
	)
	after := paths(f.search(t, Request{Pattern: "Needle three"}))
	if len(after) != 1 || after[0] != "renamed/four.go" {
		t.Errorf("after rename, files = %v, want only renamed/four.go", after)
	}
}

func TestReconcileCatchesUpOnChangesMadeWhileStopped(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "kept.go", "// Needle kept\n")
	f.write(t, "gone.go", "// Needle gone\n")
	f.rebuild(t)

	// Changes made with no service running: nothing reported them.
	f.write(t, "kept.go", "// Needle kept and edited\n")
	f.remove(t, "gone.go")
	f.write(t, "added.go", "// Needle added\n")

	state, applied, err := f.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if applied.Changed != 3 {
		t.Errorf("caught up %+v, want three paths changed", applied)
	}
	if state.FileCount != 2 {
		t.Errorf("file count = %d, want 2", state.FileCount)
	}

	found := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(found) != 2 {
		t.Errorf("files = %v, want kept.go and added.go", found)
	}
	if got := f.search(t, Request{Pattern: "Needle gone"}); len(got.Matches) != 0 {
		t.Error("a file deleted while stopped still matches")
	}
	if got := f.search(t, Request{Pattern: "kept and edited"}); len(got.Matches) != 1 {
		t.Error("an edit made while stopped was not picked up")
	}
}

func TestRestartReusesThePublishedIndex(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "alpha.go", "// Needle\n")
	first := f.rebuild(t)
	f.Close()

	// A fresh store over the same state directory is what a container restart
	// produces. It must answer from the persisted index without rebuilding.
	restarted := New(f.workspace, f.root, "test-project", smallLimits)
	t.Cleanup(restarted.Close)

	state, err := restarted.State()
	if err != nil {
		t.Fatalf("State after restart: %v", err)
	}
	if state.Generation != first.Generation {
		t.Errorf("generation = %d, want the persisted %d", state.Generation, first.Generation)
	}

	response, err := restarted.Search(context.Background(), Request{Pattern: "Needle"})
	if err != nil {
		t.Fatalf("Search after restart: %v", err)
	}
	if len(response.Matches) != 1 {
		t.Errorf("matches = %d, want 1 from the reused index", len(response.Matches))
	}
	if response.Generation != first.Generation {
		t.Error("the restarted store answered from a different generation")
	}

	// And a reconcile with nothing changed must not publish a new generation.
	after, applied, err := restarted.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile after restart: %v", err)
	}
	if applied.Changed != 0 || after.Generation != first.Generation {
		t.Errorf("an unchanged workspace produced %+v and generation %d", applied, after.Generation)
	}
}

func TestDeltaDepthEscalatesToAFullRebuild(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "base.go", "// Needle base\n")
	f.rebuild(t)

	var state State
	for i := 0; i < smallLimits.MaxDeltaGenerations+1; i++ {
		name := "file" + string(rune('a'+i)) + ".go"
		f.write(t, name, "// Needle "+name+"\n")
		state = f.update(t, Change{Path: name})
	}

	if state.DeltaDepth != 0 || !state.FullBuild {
		t.Errorf("delta depth %d, full build %v: the policy did not escalate to a rebuild",
			state.DeltaDepth, state.FullBuild)
	}
	// Escalating must not lose anything: every file is still searchable.
	if got := len(paths(f.search(t, Request{Pattern: "Needle"}))); got != smallLimits.MaxDeltaGenerations+2 {
		t.Errorf("files after escalation = %d, want %d", got, smallLimits.MaxDeltaGenerations+2)
	}
}

func TestOldGenerationsArePruned(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "a.go", "// Needle\n")
	f.rebuild(t)
	for i := 0; i < 4; i++ {
		f.write(t, "a.go", "// Needle "+string(rune('a'+i))+"\n")
		f.update(t, Change{Path: "a.go"})
	}

	entries, err := os.ReadDir(filepath.Join(f.root, generationsDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > smallLimits.KeepGenerations {
		t.Errorf("retained %d generations, want at most %d", len(entries), smallLimits.KeepGenerations)
	}
	// Pruning must never break the published generation.
	if _, err := f.Search(context.Background(), Request{Pattern: "Needle"}); err != nil {
		t.Errorf("search after pruning: %v", err)
	}
}

func TestPathGlobsFilterResults(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "root.go", "// Needle\n")
	f.write(t, "internal/deep/nested.go", "// Needle\n")
	f.write(t, "notes.md", "Needle\n")
	f.rebuild(t)

	cases := []struct {
		glob string
		want []string
	}{
		{"*.go", []string{"root.go"}},
		{"**/*.go", []string{"internal/deep/nested.go", "root.go"}},
		{"internal/**", []string{"internal/deep/nested.go"}},
		{"*.md", []string{"notes.md"}},
	}
	for _, c := range cases {
		got := paths(f.search(t, Request{Pattern: "Needle", PathGlobs: []string{c.glob}}))
		if len(got) != len(c.want) {
			t.Errorf("glob %q matched %v, want %v", c.glob, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("glob %q matched %v, want %v", c.glob, got, c.want)
				break
			}
		}
	}
}

func TestEscapingPathsAreRefusedRatherThanReinterpreted(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "inside.go", "// Needle\n")
	f.rebuild(t)

	hostile := []string{
		"/etc/passwd",
		"../outside/**",
		"..",
		"C:\\Windows\\**",
		"a/../../b",
	}
	for _, glob := range hostile {
		_, err := f.Search(context.Background(), Request{Pattern: "Needle", PathGlobs: []string{glob}})
		if err == nil {
			t.Errorf("glob %q was accepted", glob)
			continue
		}
		if got := codeOf(t, err); got != CodeInvalidPattern {
			t.Errorf("glob %q produced code %q, want %q", glob, got, CodeInvalidPattern)
		}
	}

	for _, path := range append(hostile, "/absolute.go") {
		_, _, err := f.Update(context.Background(), []Change{{Path: path}})
		if err == nil {
			t.Errorf("change path %q was accepted", path)
		}
	}
}

func TestNormalizeRelativeAcceptsOrdinaryPaths(t *testing.T) {
	for _, name := range []string{"a.go", "internal/a.go", "./a.go", "a/b/../c.go"} {
		if _, err := normalizeRelative(name); err != nil {
			t.Errorf("normalizeRelative(%q) = %v, want acceptance", name, err)
		}
	}
}

func TestRegexAndCaseSensitivity(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "a.go", "func NeedleOne() {}\nfunc needletwo() {}\n")
	f.rebuild(t)

	// Case sensitivity applies to a regex exactly as it does to a literal, so the
	// same expression matches both declarations by default and one when the
	// caller asks for case to be respected.
	if got := f.search(t, Request{Pattern: `func Needle\w+\(`, Mode: ModeRegex}); len(got.Matches) != 2 {
		t.Errorf("case-insensitive regex matched %d lines, want 2", len(got.Matches))
	}
	if got := f.search(t, Request{Pattern: `func Needle\w+\(`, Mode: ModeRegex, CaseSensitive: true}); len(got.Matches) != 1 {
		t.Errorf("case-sensitive regex matched %d lines, want 1", len(got.Matches))
	}
	if got := f.search(t, Request{Pattern: "needle"}); len(got.Matches) != 2 {
		t.Errorf("case-insensitive literal matched %d, want 2", len(got.Matches))
	}
	if got := f.search(t, Request{Pattern: "needle", CaseSensitive: true}); len(got.Matches) != 1 {
		t.Errorf("case-sensitive literal matched %d, want 1", len(got.Matches))
	}

	_, err := f.Search(context.Background(), Request{Pattern: "func Needle(", Mode: ModeRegex})
	if got := codeOf(t, err); got != CodeInvalidPattern {
		t.Errorf("an invalid regex produced %q, want %q", got, CodeInvalidPattern)
	}
	_, err = f.Search(context.Background(), Request{Pattern: "x", Mode: "fuzzy"})
	if got := codeOf(t, err); got != CodeInvalidPattern {
		t.Errorf("an unknown mode produced %q, want %q", got, CodeInvalidPattern)
	}
}

func TestPaginationWalksTheWholeResultSetExactlyOnce(t *testing.T) {
	f := newFixture(t, smallLimits)
	for i := 0; i < 12; i++ {
		f.write(t, "file"+string(rune('a'+i))+".go", "// Needle\n")
	}
	f.rebuild(t)

	first := f.search(t, Request{Pattern: "Needle", Limit: 5})
	if first.Total != 12 {
		t.Errorf("total = %d, want 12", first.Total)
	}

	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		response := f.search(t, Request{Pattern: "Needle", Limit: 5, Cursor: cursor})
		pages++
		for _, match := range response.Matches {
			seen[match.Path]++
		}
		if response.NextCursor == "" {
			break
		}
		cursor = response.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 12 {
		t.Errorf("saw %d distinct files across %d pages, want 12", len(seen), pages)
	}
	for path, count := range seen {
		if count != 1 {
			t.Errorf("%s appeared %d times", path, count)
		}
	}
}

func TestACursorFromAnotherGenerationIsRefused(t *testing.T) {
	f := newFixture(t, smallLimits)
	for i := 0; i < 4; i++ {
		f.write(t, "file"+string(rune('a'+i))+".go", "// Needle\n")
	}
	f.rebuild(t)

	first := f.search(t, Request{Pattern: "Needle", Limit: 2})
	if first.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}

	// Publishing a new generation invalidates the cursor. Paging on regardless
	// would silently skip or repeat results, so the refusal is the correct
	// answer and it must be typed.
	f.write(t, "filee.go", "// Needle\n")
	f.update(t, Change{Path: "filee.go"})

	_, err := f.Search(context.Background(), Request{Pattern: "Needle", Limit: 2, Cursor: first.NextCursor})
	if got := codeOf(t, err); got != CodeInvalidCursor {
		t.Errorf("code = %q, want %q", got, CodeInvalidCursor)
	}

	for _, bad := range []string{"not-base64!!", "YWJj"} {
		_, err := f.Search(context.Background(), Request{Pattern: "Needle", Cursor: bad})
		if got := codeOf(t, err); got != CodeInvalidCursor {
			t.Errorf("cursor %q produced %q, want %q", bad, got, CodeInvalidCursor)
		}
	}
}

func TestLimitsAreBounded(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "a.go", "// Needle\n")
	f.rebuild(t)

	_, err := f.Search(context.Background(), Request{Pattern: "Needle", Limit: MaxLimit + 1})
	if got := codeOf(t, err); got != CodeLimitExceeded {
		t.Errorf("code = %q, want %q", got, CodeLimitExceeded)
	}
	if got := f.search(t, Request{Pattern: "Needle"}); len(got.Matches) != 1 {
		t.Error("the default limit did not apply")
	}
	_, err = f.Search(context.Background(), Request{Pattern: "   "})
	if got := codeOf(t, err); got != CodeInvalidPattern {
		t.Errorf("an empty pattern produced %q, want %q", got, CodeInvalidPattern)
	}
}

func TestOversizedFilesAreSkippedAndCounted(t *testing.T) {
	f := newFixture(t, Limits{MaxFileBytes: 64, MaxDeltaGenerations: 3, KeepGenerations: 2})
	f.write(t, "small.go", "// Needle\n")
	f.write(t, "big.go", "// Needle "+strings.Repeat("x", 200)+"\n")

	state := f.rebuild(t)
	if state.SkippedBig != 1 {
		t.Errorf("skipped = %d, want 1", state.SkippedBig)
	}
	got := paths(f.search(t, Request{Pattern: "Needle"}))
	if len(got) != 1 || got[0] != "small.go" {
		t.Errorf("files = %v, want only small.go", got)
	}
}

func TestPublicationIsAtomicUnderACorruptedNewGeneration(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "a.go", "// Needle\n")
	first := f.rebuild(t)

	// Simulate a generation directory that exists but is unusable. The published
	// link still points at the good generation, so search must keep working.
	broken := filepath.Join(f.root, generationsDir, generationName(first.Generation+1))
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, stateName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := f.search(t, Request{Pattern: "Needle"}); len(got.Matches) != 1 {
		t.Error("an unpublished broken generation affected the published one")
	}
	if state, err := f.State(); err != nil || state.Generation != first.Generation {
		t.Errorf("state = %+v, err = %v", state, err)
	}
}

func TestAnUnreadablePublishedStateIsReportedAsCorrupt(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "a.go", "// Needle\n")
	state := f.rebuild(t)
	f.Close()

	published := filepath.Join(f.root, generationsDir, generationName(state.Generation), stateName)
	if err := os.WriteFile(published, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A corrupt index is an error, not a silent empty result: answering "no
	// matches" would look like a correct answer about the project.
	_, err := f.Search(context.Background(), Request{Pattern: "Needle"})
	if got := codeOf(t, err); got != CodeIndexCorrupt {
		t.Fatalf("code = %q, want %q", got, CodeIndexCorrupt)
	}
	var typed *Error
	errors.As(err, &typed)
	if typed.Retryable {
		t.Error("a corrupt index must not be reported as retryable")
	}

	// And it must be recoverable by rebuilding rather than by hand.
	if _, err := f.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild after corruption: %v", err)
	}
	if got := f.search(t, Request{Pattern: "Needle"}); len(got.Matches) != 1 {
		t.Error("the index did not recover after a rebuild")
	}
}

func TestGlobTranslation(t *testing.T) {
	cases := map[string]string{
		"*.go":          `^[^/]*\.go$`,
		"**/*.go":       `^(?:.*/)?[^/]*\.go$`,
		"internal/**":   `^internal/.*$`,
		"a?c.go":        `^a[^/]c\.go$`,
		"[abc]/x.go":    `^[abc]/x\.go$`,
		"[!abc]/x.go":   `^[^abc]/x\.go$`,
		"pkg/(v1)/a.go": `^pkg/\(v1\)/a\.go$`,
	}
	for glob, want := range cases {
		got, err := globToRegexp(glob)
		if err != nil {
			t.Errorf("globToRegexp(%q) = %v", glob, err)
			continue
		}
		if got != want {
			t.Errorf("globToRegexp(%q) = %q, want %q", glob, got, want)
		}
	}
	for _, bad := range []string{"", "   ", "[abc/x.go", "[]/x.go"} {
		if _, err := globToRegexp(bad); err == nil {
			t.Errorf("globToRegexp(%q) was accepted", bad)
		}
	}
}

func TestPreviewIsBoundedAndKeepsTheMatch(t *testing.T) {
	prefix := strings.Repeat("a", 900)
	line := prefix + "NEEDLE" + strings.Repeat("b", 900)
	preview := boundedPreview(line, len(prefix), len(prefix)+6)
	if len(preview) > maxPreviewBytes {
		t.Errorf("preview is %d bytes, want at most %d", len(preview), maxPreviewBytes)
	}
	if !strings.Contains(preview, "NEEDLE") {
		t.Error("the bounded preview does not contain the match")
	}
	short := "func Needle() {}"
	if boundedPreview(short, 5, 11) != short {
		t.Error("a short line was altered")
	}
}
