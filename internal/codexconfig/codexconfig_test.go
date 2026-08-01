package codexconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// realistic mirrors the shapes an installed Codex configuration actually
// contains: nested tables, a server with a sub-table, and table keys that are
// quoted Windows paths. LCTK writes into a file with content like this, so the
// merge must be provably harmless to all of it.
const realistic = `# A file the user owns.
model = "gpt-5"

[features]
web_search = true

[mcp_servers.node_repl]
command = "node"
args = ["--experimental-repl"]

[mcp_servers.node_repl.env]
NODE_OPTIONS = "--max-old-space-size=4096"

[projects."e:\\work\\example-game"]
trust_level = "trusted"

[projects.'c:\work\example-repo']
trust_level = "trusted"
`

func entryFor(projectID, url string) Entry {
	return Entry{
		Name:              EntryName(projectID),
		URL:               url,
		BearerTokenEnvVar: "LCTK_TOKEN_" + strings.ToUpper(strings.ReplaceAll(projectID, "-", "_")),
		Enabled:           true,
	}
}

func mustMerge(t *testing.T, document, projectID string, entry Entry, force bool) string {
	t.Helper()
	merged, err := Merge(document, projectID, entry, force)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return merged
}

func parse(t *testing.T, document string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := toml.Unmarshal([]byte(document), &out); err != nil {
		t.Fatalf("result is not valid TOML: %v\n%s", err, document)
	}
	return out
}

func serverTable(t *testing.T, document, name string) map[string]any {
	t.Helper()
	parsed := parse(t, document)
	servers, ok := parsed["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("no mcp_servers table in:\n%s", document)
	}
	entry, ok := servers[name].(map[string]any)
	if !ok {
		t.Fatalf("no mcp_servers.%s table in:\n%s", name, document)
	}
	return entry
}

func TestEntryNameIsBareAndPerProject(t *testing.T) {
	first := EntryName("alpha-abcd1234")
	second := EntryName("beta-abcd1234")
	if first == second {
		t.Error("two projects share a Codex entry name")
	}
	if !strings.HasPrefix(first, EntryPrefix) {
		t.Errorf("name = %q, want the LCTK prefix so the entry is attributable", first)
	}
	for _, r := range first {
		bare := r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !bare {
			t.Errorf("name %q contains %q, which is not a TOML bare key character", first, r)
		}
	}
}

func TestRenderEscapesHostileValues(t *testing.T) {
	entry := Entry{
		Name:              `lctk_x`,
		URL:               `http://127.0.0.1:4444/projects/a"b\c/mcp`,
		BearerTokenEnvVar: "LCTK_TOKEN_A\tB",
		Enabled:           true,
	}
	document := entry.Render()
	table := serverTable(t, document, "lctk_x")
	if table["url"] != `http://127.0.0.1:4444/projects/a"b\c/mcp` {
		t.Errorf("url round-tripped as %q", table["url"])
	}
	if table["bearer_token_env_var"] != "LCTK_TOKEN_A\tB" {
		t.Errorf("variable round-tripped as %q", table["bearer_token_env_var"])
	}
}

func TestRenderCarriesNoToken(t *testing.T) {
	// The credential must be unrepresentable, not merely omitted, so a future
	// change cannot quietly start writing it.
	document := entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp").Render()
	for _, forbidden := range []string{"bearer_token =", "token =", "authorization"} {
		if strings.Contains(strings.ToLower(document), forbidden) {
			t.Errorf("rendered entry contains %q:\n%s", forbidden, document)
		}
	}
}

func TestMergeAppendsWithoutDisturbingOtherContent(t *testing.T) {
	merged := mustMerge(t, realistic, "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), false)

	for _, line := range strings.Split(strings.TrimSuffix(realistic, "\n"), "\n") {
		if !strings.Contains(merged, line) {
			t.Errorf("merge lost the original line %q", line)
		}
	}
	if !strings.HasPrefix(merged, realistic[:strings.Index(realistic, "\n")]) {
		t.Error("merge did not preserve the start of the document")
	}

	parsed := parse(t, merged)
	servers := parsed["mcp_servers"].(map[string]any)
	if _, ok := servers["node_repl"]; !ok {
		t.Error("the user's own server disappeared")
	}
	if _, ok := servers[EntryName("alpha-abcd1234")]; !ok {
		t.Error("the LCTK entry is absent")
	}
	projects := parsed["projects"].(map[string]any)
	if len(projects) != 2 {
		t.Errorf("project trust entries = %d, want 2", len(projects))
	}
}

func TestMergeIsIdempotent(t *testing.T) {
	entry := entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp")
	once := mustMerge(t, realistic, "alpha-abcd1234", entry, false)
	twice := mustMerge(t, once, "alpha-abcd1234", entry, false)
	if once != twice {
		t.Errorf("a second merge changed the document:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
	if strings.Count(twice, beginMarker("alpha-abcd1234")) != 1 {
		t.Error("the managed region was duplicated")
	}
}

func TestMergeRewritesTheManagedRegionInPlace(t *testing.T) {
	first := mustMerge(t, realistic, "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), false)
	second := mustMerge(t, first, "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:9999/projects/alpha-abcd1234/mcp"), false)

	if strings.Count(second, beginMarker("alpha-abcd1234")) != 1 {
		t.Error("rewriting produced a second region rather than replacing the first")
	}
	table := serverTable(t, second, EntryName("alpha-abcd1234"))
	if table["url"] != "http://127.0.0.1:9999/projects/alpha-abcd1234/mcp" {
		t.Errorf("url = %v, want the rewritten address", table["url"])
	}
	if !strings.Contains(second, "[mcp_servers.node_repl.env]") {
		t.Error("rewriting disturbed an unrelated server")
	}
}

func TestTwoProjectsCoexist(t *testing.T) {
	merged := mustMerge(t, realistic, "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), false)
	merged = mustMerge(t, merged, "beta-abcd1234",
		entryFor("beta-abcd1234", "http://127.0.0.1:4444/projects/beta-abcd1234/mcp"), false)

	servers := parse(t, merged)["mcp_servers"].(map[string]any)
	for _, name := range []string{"node_repl", EntryName("alpha-abcd1234"), EntryName("beta-abcd1234")} {
		if _, ok := servers[name]; !ok {
			t.Errorf("server %q is missing", name)
		}
	}
	if Locate(merged, "alpha-abcd1234").Placement != PlacementManaged {
		t.Error("the first project's region is no longer recognized")
	}
	if Locate(merged, "beta-abcd1234").Placement != PlacementManaged {
		t.Error("the second project's region is no longer recognized")
	}
}

func TestMergeRefusesAForeignEntry(t *testing.T) {
	name := EntryName("alpha-abcd1234")
	document := realistic + "\n[mcp_servers." + name + "]\nurl = \"http://elsewhere/mcp\"\n"

	if got := Locate(document, "alpha-abcd1234").Placement; got != PlacementForeign {
		t.Fatalf("placement = %q, want foreign", got)
	}
	_, err := Merge(document, "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), false)
	if !errors.Is(err, ErrForeignEntry) {
		t.Fatalf("err = %v, want ErrForeignEntry", err)
	}

	forced := mustMerge(t, document, "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), true)
	table := serverTable(t, forced, name)
	if table["url"] != "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp" {
		t.Errorf("url = %v, want the LCTK endpoint after --force", table["url"])
	}
	if !strings.Contains(forced, "[mcp_servers.node_repl]") {
		t.Error("forcing removed an unrelated server")
	}
}

func TestForceReplacesAForeignEntryIncludingItsSubTables(t *testing.T) {
	name := EntryName("alpha-abcd1234")
	document := "[mcp_servers." + name + "]\n" +
		"url = \"http://elsewhere/mcp\"\n\n" +
		"[mcp_servers." + name + ".http_headers]\n" +
		"X-Old = \"1\"\n\n" +
		"[features]\nweb_search = true\n"

	forced := mustMerge(t, document, "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), true)
	if strings.Contains(forced, "X-Old") {
		t.Errorf("the replaced entry left its sub-table behind:\n%s", forced)
	}
	if _, ok := parse(t, forced)["features"]; !ok {
		t.Error("replacement consumed the following table")
	}
}

func TestMergeRefusesAnInlineForeignEntry(t *testing.T) {
	name := EntryName("alpha-abcd1234")
	document := "[mcp_servers]\n" + name + " = { url = \"http://elsewhere/mcp\" }\n"

	if got := Locate(document, "alpha-abcd1234").Placement; got != PlacementForeignInline {
		t.Fatalf("placement = %q, want foreign_inline", got)
	}
	for _, force := range []bool{false, true} {
		_, err := Merge(document, "alpha-abcd1234",
			entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), force)
		if !errors.Is(err, ErrForeignInlineEntry) {
			t.Errorf("force=%v: err = %v, want ErrForeignInlineEntry", force, err)
		}
	}
}

func TestMergeRefusesAnAlreadyBrokenDocument(t *testing.T) {
	// A file Codex cannot load is one LCTK must not silently rewrite: the
	// operator would see LCTK's entry appear and still have no servers.
	_, err := Merge("[mcp_servers.broken\nurl = ", "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), false)
	if !errors.Is(err, ErrExistingInvalid) {
		t.Fatalf("err = %v, want ErrExistingInvalid", err)
	}
}

func TestMergeIntoAnEmptyDocument(t *testing.T) {
	merged := mustMerge(t, "", "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), false)
	if got := serverTable(t, merged, EntryName("alpha-abcd1234"))["enabled"]; got != true {
		t.Errorf("enabled = %v", got)
	}
}

func TestMergeRejectsAnEntryWithoutACredentialReference(t *testing.T) {
	entry := entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp")
	entry.BearerTokenEnvVar = ""
	if _, err := Merge(realistic, "alpha-abcd1234", entry, false); err == nil {
		t.Fatal("an entry with no credential reference was accepted")
	}
}

func TestRemoveDropsOnlyTheManagedRegion(t *testing.T) {
	merged := mustMerge(t, realistic, "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), false)

	removed, changed, err := Remove(merged, "alpha-abcd1234")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !changed {
		t.Fatal("Remove reported no change")
	}
	if strings.Contains(removed, EntryName("alpha-abcd1234")) {
		t.Errorf("the entry survived removal:\n%s", removed)
	}
	if strings.TrimSpace(removed) != strings.TrimSpace(realistic) {
		t.Errorf("removal did not restore the original document:\n%s", removed)
	}

	if _, changed, err := Remove(removed, "alpha-abcd1234"); err != nil || changed {
		t.Errorf("removing twice: changed = %v, err = %v", changed, err)
	}
}

func TestRemoveLeavesAForeignEntryAlone(t *testing.T) {
	name := EntryName("alpha-abcd1234")
	document := realistic + "\n[mcp_servers." + name + "]\nurl = \"http://elsewhere/mcp\"\n"
	removed, changed, err := Remove(document, "alpha-abcd1234")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if changed || removed != document {
		t.Error("removal touched an entry LCTK did not write")
	}
}

func TestHomeHonorsTheCodexOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	home, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	if home != dir {
		t.Errorf("home = %q, want %q", home, dir)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, FileName) {
		t.Errorf("path = %q", path)
	}
}

func TestReadMissingFileIsNotAnError(t *testing.T) {
	document, err := ReadFile(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if document != "" {
		t.Errorf("document = %q, want empty", document)
	}
}

func TestWriteBacksUpThePreviousContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(realistic), 0o644); err != nil {
		t.Fatal(err)
	}

	merged := mustMerge(t, realistic, "alpha-abcd1234",
		entryFor("alpha-abcd1234", "http://127.0.0.1:4444/projects/alpha-abcd1234/mcp"), false)
	backup, err := WriteFile(path, merged)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if backup != path+BackupSuffix {
		t.Errorf("backup = %q", backup)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != merged {
		t.Error("the written file does not match the merged document")
	}
	saved, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != realistic {
		t.Error("the backup does not hold the previous contents")
	}
}

func TestWriteCreatesAMissingFileWithoutABackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	backup, err := WriteFile(path, "model = \"gpt-5\"\n")
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if backup != "" {
		t.Errorf("backup = %q, want none for a file that did not exist", backup)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file was not created: %v", err)
	}
}
