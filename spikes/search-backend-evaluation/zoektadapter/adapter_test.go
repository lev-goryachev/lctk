package zoektadapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestWorkingTreeLifecycle(t *testing.T) {
	workspace := t.TempDir()
	indexDir := t.TempDir()
	writeFixture(t, workspace)
	adapter := Indexer{Workspace: workspace, IndexDir: indexDir, ProjectID: "alpha"}

	initial, err := adapter.Full(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if initial.Generation != 1 || len(initial.Files) != 5 {
		t.Fatalf("unexpected initial manifest: %+v", initial)
	}
	before := shardSnapshot(t, indexDir)
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "alpha_token", Mode: "literal", CaseSensitive: true}),
		"README.md", "src/main.go")
	assertPaths(t, runSearch(t, adapter, Request{Pattern: `alpha_[a-z]+`, Mode: "regex", CaseSensitive: true, Languages: []string{"Go"}}),
		"src/main.go")
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "shared_token", PathGlobs: []string{"**/*.go"}}),
		"src/main.go")
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "ignored_token"}))

	write(t, workspace, "src/main.go", "package main\n// modified_token\n")
	write(t, workspace, "src/new.go", "package main\n// created_token\n")
	if err := os.Remove(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(workspace, "scripts", "build.py"), filepath.Join(workspace, "scripts", "renamed.py")); err != nil {
		t.Fatal(err)
	}
	updated, err := adapter.Apply(context.Background(), []Change{
		{Path: "src/main.go"},
		{Path: "src/new.go"},
		{Path: "README.md", Deleted: true},
		{Path: "scripts/build.py", Deleted: true},
		{Path: "scripts/renamed.py"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation != 2 || len(updated.Files) != 5 {
		t.Fatalf("unexpected updated manifest: %+v", updated)
	}
	after := shardSnapshot(t, indexDir)
	if len(after) <= len(before) {
		t.Fatalf("delta update did not add bounded persistent state: before=%v after=%v", before, after)
	}
	for name, size := range before {
		if after[name] != size {
			t.Fatalf("base shard %q changed during delta update: before=%d after=%d", name, size, after[name])
		}
	}
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "alpha_token"}))
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "modified_token"}), "src/main.go")
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "created_token"}), "src/new.go")
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "python_token"}), "scripts/renamed.py")

	write(t, workspace, "offline.txt", "offline_token\n")
	write(t, workspace, "src/new.go", "package main\n// offline_modified_token\n")
	reconciled, changes, err := adapter.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Generation != 3 {
		t.Fatalf("reconcile generation = %d, want 3", reconciled.Generation)
	}
	assertChangePaths(t, changes, "offline.txt", "src/new.go")
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "offline_token"}), "offline.txt")
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "offline_modified_token"}), "src/new.go")
}

func TestPersistenceIsolationAndPagination(t *testing.T) {
	root := t.TempDir()
	alphaWorkspace := filepath.Join(root, "alpha-workspace")
	betaWorkspace := filepath.Join(root, "beta-workspace")
	alpha := Indexer{Workspace: alphaWorkspace, IndexDir: filepath.Join(root, "alpha-index"), ProjectID: "alpha"}
	beta := Indexer{Workspace: betaWorkspace, IndexDir: filepath.Join(root, "beta-index"), ProjectID: "beta"}
	write(t, alphaWorkspace, "a.txt", "page_token page_token\nalpha_only\n")
	write(t, betaWorkspace, "b.txt", "page_token\nbeta_only\n")
	if _, err := alpha.Full(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := beta.Full(context.Background()); err != nil {
		t.Fatal(err)
	}

	first := runSearch(t, alpha, Request{Pattern: "page_token", Limit: 1})
	if len(first.Matches) != 1 || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v", first)
	}
	second := runSearch(t, alpha, Request{Pattern: "page_token", Limit: 1, Cursor: first.NextCursor})
	if len(second.Matches) != 1 || second.NextCursor != "" {
		t.Fatalf("unexpected second page: %+v", second)
	}
	assertPaths(t, runSearch(t, alpha, Request{Pattern: "beta_only"}))
	assertPaths(t, runSearch(t, beta, Request{Pattern: "alpha_only"}))

	// A fresh adapter instance proves process-independent index reuse.
	restarted := Indexer{Workspace: alphaWorkspace, IndexDir: alpha.IndexDir, ProjectID: "alpha"}
	assertPaths(t, runSearch(t, restarted, Request{Pattern: "alpha_only"}), "a.txt")

	write(t, alphaWorkspace, "new.txt", "new_generation\n")
	if _, err := alpha.Apply(context.Background(), []Change{{Path: "new.txt"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := alpha.Search(context.Background(), Request{Pattern: "page_token", Cursor: first.NextCursor}); err == nil || !strings.Contains(err.Error(), "INVALID_CURSOR") {
		t.Fatalf("stale cursor error = %v, want INVALID_CURSOR", err)
	}
}

func TestTypedValidationFailures(t *testing.T) {
	adapter := Indexer{Workspace: t.TempDir(), IndexDir: t.TempDir(), ProjectID: "alpha"}
	write(t, adapter.Workspace, "a.txt", "token\n")
	if _, err := adapter.Full(context.Background()); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		request Request
		code    string
	}{
		{Request{}, "INVALID_PATTERN"},
		{Request{Pattern: "[", Mode: "regex"}, "INVALID_PATTERN"},
		{Request{Pattern: "token", Limit: 1001}, "LIMIT_EXCEEDED"},
		{Request{Pattern: "token", Cursor: "not-base64"}, "INVALID_CURSOR"},
	}
	for _, tc := range cases {
		_, err := adapter.Search(context.Background(), tc.request)
		requireErrorCode(t, err, tc.code)
	}
	_, err := (Indexer{Workspace: t.TempDir(), IndexDir: filepath.Join(t.TempDir(), "missing"), ProjectID: "missing"}).Search(
		context.Background(), Request{Pattern: "token"})
	requireErrorCode(t, err, "INDEX_NOT_READY")
	if err := os.WriteFile(filepath.Join(adapter.IndexDir, manifestName), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Search(context.Background(), Request{Pattern: "token"})
	requireErrorCode(t, err, "INDEX_CORRUPT")
}

func TestExclusionsGlobsLanguagesAndBounds(t *testing.T) {
	workspace := t.TempDir()
	indexDir := t.TempDir()
	longPrefix := strings.Repeat("x", 600)
	write(t, workspace, "src/router.ts", "const filter_token = 1\n")
	write(t, workspace, "src/nested/handler.ts", longPrefix+" filter_token\n")
	write(t, workspace, "src/router.js", "const filter_token = 1\n")
	write(t, workspace, "docs/filter.md", "filter_token\n")
	for _, directory := range []string{".git", ".hg", ".svn", "node_modules", "dist", "build", "coverage", ".venv", "vendor", "generated"} {
		write(t, workspace, directory+"/ignored.txt", "excluded_token\n")
	}
	adapter := Indexer{Workspace: workspace, IndexDir: indexDir, ProjectID: "filters"}
	manifest, err := adapter.Full(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 4 {
		t.Fatalf("included files = %v, want four source files", sortedKeys(manifest.Files))
	}
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "excluded_token"}))
	response := runSearch(t, adapter, Request{
		Pattern: "filter_token", PathGlobs: []string{"src/**/*.ts"}, Languages: []string{"typescript"},
	})
	assertPaths(t, response, "src/nested/handler.ts", "src/router.ts")
	for _, match := range response.Matches {
		if match.Line != 1 || match.Column < 1 {
			t.Fatalf("position is not one-based: %+v", match)
		}
		if len(match.Preview) > maxPreviewBytes {
			t.Fatalf("preview has %d bytes, maximum is %d", len(match.Preview), maxPreviewBytes)
		}
	}
	assertPaths(t, runSearch(t, adapter, Request{Pattern: "filter_token", PathGlobs: []string{"src/*.ts"}}), "src/router.ts")
	_, err = adapter.Search(context.Background(), Request{Pattern: "filter_token", PathGlobs: []string{"../outside/**"}})
	requireErrorCode(t, err, "INVALID_PATTERN")
	if _, err := adapter.Apply(context.Background(), []Change{{Path: "../outside.txt"}}); err == nil {
		t.Fatal("path escape unexpectedly accepted")
	}
}

func BenchmarkLiteralSearch(b *testing.B) {
	workspace := b.TempDir()
	indexDir := b.TempDir()
	for n := 0; n < 1000; n++ {
		writeBenchmarkFile(b, workspace, fmt.Sprintf("src/file-%04d.go", n), fmt.Sprintf("package fixture\n// shared_token_%04d\n", n))
	}
	adapter := Indexer{Workspace: workspace, IndexDir: indexDir, ProjectID: "benchmark"}
	if _, err := adapter.Full(context.Background()); err != nil {
		b.Fatal(err)
	}
	session, err := adapter.OpenSession()
	if err != nil {
		b.Fatal(err)
	}
	defer session.Close()
	b.ResetTimer()
	for range b.N {
		if _, err := session.Search(context.Background(), Request{Pattern: "shared_token_0999", Limit: 20}); err != nil {
			b.Fatal(err)
		}
	}
}

func writeFixture(t *testing.T, root string) {
	t.Helper()
	write(t, root, "README.md", "alpha_token shared_token\n")
	write(t, root, "src/main.go", "package main\n// alpha_token shared_token\n")
	write(t, root, "src/other.rs", "// rust_token\n")
	write(t, root, "scripts/build.py", "# python_token\n")
	write(t, root, "assets/data.json", "{\"json_token\": true}\n")
	write(t, root, ".git/ignored.txt", "ignored_token\n")
	write(t, root, "node_modules/pkg/index.js", "ignored_token\n")
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeBenchmarkFile(b *testing.B, root, name, content string) {
	b.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
		b.Fatal(err)
	}
}

func runSearch(t *testing.T, adapter Indexer, request Request) Response {
	t.Helper()
	response, err := adapter.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertPaths(t *testing.T, response Response, want ...string) {
	t.Helper()
	got := make([]string, 0, len(response.Matches))
	for _, match := range response.Matches {
		got = append(got, match.Path)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func assertChangePaths(t *testing.T, changes []Change, want ...string) {
	t.Helper()
	got := make([]string, 0, len(changes))
	for _, change := range changes {
		got = append(got, change.Path)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("changes = %v, want %v", got, want)
	}
}

func requireErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want typed code %s", err, code)
	}
}

func shardSnapshot(t *testing.T, indexDir string) map[string]int64 {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(indexDir, "*.zoekt"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]int64, len(matches))
	for _, name := range matches {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		result[filepath.Base(name)] = info.Size()
	}
	return result
}
