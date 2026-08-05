package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/symbols"
)

func TestGraphPublishesQueriesAndRetractsWithSemanticGeneration(t *testing.T) {
	ctx := context.Background()
	source := &sourceStub{files: map[string][]byte{
		"src/main.js": []byte("import { Work } from './dep.js';\nfunction Run(){ Work(); }\n"),
		"src/dep.js":  []byte("export function Work(){ return 1; }\n"),
	}}
	engine, err := symbols.New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	embedder := &deterministicEmbedder{dimension: 16}
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "semantic.db"), Model: "test", Dimensions: 16}, source, engine, embedder)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Sync(ctx, exactState(1, source)); err != nil {
		t.Fatalf("Sync generation 1: %v", err)
	}
	status, err := store.GraphStatus()
	if err != nil || !status.Ready || status.Generation != 1 || status.FileCount != 2 || status.CallCount != 1 {
		t.Fatalf("graph status = %+v, err = %v", status, err)
	}
	callers, err := store.Callers(ctx, GraphRequest{Name: "Work", Limit: 1})
	if err != nil || len(callers.Matches) != 1 || callers.Matches[0].Caller != "Run" {
		t.Fatalf("callers = %+v, err = %v", callers, err)
	}
	callees, err := store.Callees(ctx, GraphRequest{Name: "Run"})
	if err != nil || len(callees.Matches) != 1 || callees.Matches[0].Callee != "Work" {
		t.Fatalf("callees = %+v, err = %v", callees, err)
	}
	dependency, err := store.DependencyPath(ctx, DependencyRequest{From: "src/main.js", To: "src/dep.js"})
	if err != nil || !dependency.Found || len(dependency.Path) != 2 {
		t.Fatalf("dependency = %+v, err = %v", dependency, err)
	}
	impact, err := store.Impact(ctx, ImpactRequest{Target: "Work"})
	if err != nil || len(impact.Calls) != 1 || impact.Files[0] != "src/main.js" {
		t.Fatalf("impact = %+v, err = %v", impact, err)
	}
	repositoryMap, err := store.RepositoryMap(ctx, MapRequest{MaxChars: 2048})
	if err != nil || !strings.Contains(repositoryMap.Map, "src/main.js") || !strings.Contains(repositoryMap.Map, "function Run") {
		t.Fatalf("repository map = %+v, err = %v", repositoryMap, err)
	}

	source.files["src/main.js"] = []byte("import { Work } from './dep.js';\nfunction Run(){ Other(); }\n")
	if _, err := store.Sync(ctx, exactState(2, source)); err != nil {
		t.Fatalf("Sync generation 2: %v", err)
	}
	callers, err = store.Callers(ctx, GraphRequest{Name: "Work"})
	if err != nil || callers.Total != 0 {
		t.Fatalf("retracted callers = %+v, err = %v", callers, err)
	}
	delete(source.files, "src/dep.js")
	if _, err := store.Sync(ctx, exactState(3, source)); err != nil {
		t.Fatalf("Sync generation 3: %v", err)
	}
	status, _ = store.GraphStatus()
	if status.FileCount != 1 || status.Generation != 3 {
		t.Fatalf("post-delete graph status = %+v", status)
	}
}

func TestSchemaOneMigratesWithoutDiscardingSemanticState(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "semantic.db")
	source := &sourceStub{files: map[string][]byte{"a.go": []byte("package a\nfunc A(){}\n")}}
	engine, err := symbols.New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	embedder := &deterministicEmbedder{dimension: 16}
	store, err := Open(Config{Path: database, Model: "test", Dimensions: 16}, source, engine, embedder)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sync(ctx, exactState(1, source)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE graph_calls; DROP TABLE graph_imports; DROP TABLE graph_nodes; DROP TABLE graph_files; PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Config{Path: database, Model: "test", Dimensions: 16}, source, engine, embedder)
	if err != nil {
		t.Fatalf("migrate schema 1: %v", err)
	}
	defer reopened.Close()
	semanticStatus, err := reopened.Status()
	if err != nil || semanticStatus.Generation != 1 || semanticStatus.ChunkCount == 0 {
		t.Fatalf("semantic state after migration = %+v, err = %v", semanticStatus, err)
	}
	if _, err := reopened.Sync(ctx, exactState(1, source)); err != nil {
		t.Fatalf("rebuild graph after migration: %v", err)
	}
	graphStatus, err := reopened.GraphStatus()
	if err != nil || !graphStatus.Ready || graphStatus.Generation != 1 || graphStatus.NodeCount == 0 {
		t.Fatalf("graph after migration = %+v, err = %v", graphStatus, err)
	}
}

func TestFailedSchemaOneActivationRestoresOriginalDatabase(t *testing.T) {
	database := filepath.Join(t.TempDir(), "semantic.db")
	source := &sourceStub{files: map[string][]byte{"a.go": []byte("package a\nfunc A(){}\n")}}
	embedder := &deterministicEmbedder{dimension: 16}
	store, err := Open(Config{Path: database, Model: "model-a", Dimensions: 16}, source, outlineStub{}, embedder)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE graph_calls; DROP TABLE graph_imports; DROP TABLE graph_nodes; DROP TABLE graph_files; PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(Config{Path: database, Model: "model-b", Dimensions: 16}, source, outlineStub{}, embedder)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeModelMismatch {
		t.Fatalf("Open incompatible migration error = %v, want MODEL_MISMATCH", err)
	}
	if _, err := os.Stat(database + ".failed-v2"); err != nil {
		t.Fatalf("failed migrated database was not preserved: %v", err)
	}

	legacy, err := openMigrationDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	var version int
	if err := legacy.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("restored database schema = %d, want 1", version)
	}
}

func TestInterruptedPreCommitMigrationRetriesWithoutLosingTheActiveDatabase(t *testing.T) {
	database := filepath.Join(t.TempDir(), "semantic.db")
	source := &sourceStub{files: map[string][]byte{"a.go": []byte("package a\nfunc A(){}\n")}}
	embedder := &deterministicEmbedder{dimension: 16}
	store, err := Open(Config{Path: database, Model: "model-a", Dimensions: 16}, source, outlineStub{}, embedder)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE graph_calls; DROP TABLE graph_imports; DROP TABLE graph_nodes; DROP TABLE graph_files; PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// This is the only pre-commit crash state after the durable migration copy
	// exists and the original v1 inode has received its rollback name.
	if err := copyDatabaseFile(database, database+".migration"); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(database, database+".rollback-v1"); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Config{Path: database, Model: "model-a", Dimensions: 16}, source, outlineStub{}, embedder)
	if err != nil {
		t.Fatalf("retry interrupted migration: %v", err)
	}
	defer reopened.Close()
	var version int
	if err := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("retried migration schema = %d, want %d", version, schemaVersion)
	}
	if _, err := os.Stat(database + ".migration"); !os.IsNotExist(err) {
		t.Fatalf("interrupted migration copy remains: %v", err)
	}
	activeInfo, err := os.Stat(database)
	if err != nil {
		t.Fatal(err)
	}
	rollbackInfo, err := os.Stat(database + ".rollback-v1")
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(activeInfo, rollbackInfo) {
		t.Fatal("migration did not atomically replace the active v1 inode")
	}
}

func TestCallPaginationAndDuplicateDeclarationAmbiguity(t *testing.T) {
	ctx := context.Background()
	source := &sourceStub{files: map[string][]byte{
		"one.js": []byte("function One(){ Work(); }\n"),
		"two.js": []byte("function Two(){ Work(); }\n"),
		"dep.js": []byte("export function Work(){}\n"),
		"dep.ts": []byte("export function Work(): void {}\n"),
	}}
	engine, err := symbols.New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "semantic.db"), Model: "test", Dimensions: 16},
		source, engine, &deterministicEmbedder{dimension: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Sync(ctx, exactState(1, source)); err != nil {
		t.Fatal(err)
	}
	first, err := store.Callers(ctx, GraphRequest{Name: "Work", Limit: 1})
	if err != nil || !first.Ambiguous || first.Declarations != 2 || !first.Truncated || first.NextCursor == "" || len(first.Matches) != 1 {
		t.Fatalf("first page = %+v, err = %v", first, err)
	}
	second, err := store.Callers(ctx, GraphRequest{Name: "Work", Limit: 1, Cursor: first.NextCursor})
	if err != nil || second.Truncated || len(second.Matches) != 1 || second.Matches[0].Path == first.Matches[0].Path {
		t.Fatalf("second page = %+v, err = %v", second, err)
	}
}

func TestRepositoryMapBudgetCountsUnicodeCharacters(t *testing.T) {
	var builder strings.Builder
	used := 0
	if !appendMapText(&builder, &used, 3, "é界a") {
		t.Fatal("three Unicode characters did not fit a three-character budget")
	}
	if appendMapText(&builder, &used, 3, "b") {
		t.Fatal("text exceeded the exact character budget")
	}
	if used != 3 || builder.String() != "é界a" {
		t.Fatalf("used=%d map=%q", used, builder.String())
	}
}
