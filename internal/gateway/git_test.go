package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/gitinfo"
)

// fakeGit records which root it was asked about, which is how the scope
// assertions below can tell that the route decided and the arguments did not.
type fakeGit struct {
	roots  []string
	status gitinfo.Status
	diff   gitinfo.Diff
	err    error
}

func (f *fakeGit) Status(_ context.Context, root string, _ gitinfo.Options) (gitinfo.Status, error) {
	f.roots = append(f.roots, root)
	return f.status, f.err
}

func (f *fakeGit) Diff(_ context.Context, root string, options gitinfo.DiffOptions) (gitinfo.Diff, error) {
	f.roots = append(f.roots, root)
	if f.err != nil {
		return gitinfo.Diff{}, f.err
	}
	diff := f.diff
	diff.Staged = options.Staged
	return diff, nil
}

func callTool[T any](t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) (T, *mcp.CallToolResult) {
	t.Helper()
	var output T
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s transport failure: %v", name, err)
	}
	if result.IsError {
		return output, result
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("%s output is not the expected shape: %v", name, err)
	}
	return output, result
}

func gitFixture(t *testing.T, git *fakeGit) *fixture {
	t.Helper()
	f := newFixture(t, true, "alpha-aaaaaaaa")
	f.git = git
	return f
}

func TestGitStatusReportsTheWorkingTree(t *testing.T) {
	git := &fakeGit{status: gitinfo.Status{
		Repository: true, Branch: "main", Commit: "abc123", ShortCommit: "abc123",
		Dirty: true, Total: 2,
		Changed: []gitinfo.Change{
			{Path: "a.go", State: "modified", WorkingTree: true},
			{Path: "b.go", State: "untracked", WorkingTree: true},
		},
	}}
	f := gitFixture(t, git)
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	output, _ := callTool[gitStatusOutput](t, session, "git_status", nil)
	if !output.Repository || output.Branch != "main" || !output.Dirty {
		t.Fatalf("output = %+v", output)
	}
	if len(output.Changed) != 2 || output.Total != 2 {
		t.Fatalf("changed = %+v, total = %d", output.Changed, output.Total)
	}
	if output.ScopeSource != "route_and_registry" {
		t.Fatalf("scope_source = %q", output.ScopeSource)
	}
}

// The same guarantee the other tools carry: a model-supplied project or path
// does not decide what is read.
func TestGitStatusIgnoresScopeLikeArguments(t *testing.T) {
	git := &fakeGit{status: gitinfo.Status{Repository: true, Branch: "main"}}
	f := gitFixture(t, git)
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	output, _ := callTool[gitStatusOutput](t, session, "git_status", map[string]any{
		"project_id":      "beta-bbbbbbbb",
		"repository_root": "/etc",
		"path":            "../../elsewhere",
	})
	if output.ProjectID != "alpha-aaaaaaaa" {
		t.Fatalf("project_id = %q, want the routed project", output.ProjectID)
	}
	for _, root := range git.roots {
		if root != "/work/alpha-aaaaaaaa" {
			t.Fatalf("git was asked about %q, which is not the routed project's path", root)
		}
	}
}

// A folder that is not a repository is an answer, not a failure. Plenty of
// projects are just folders.
func TestAProjectThatIsNotARepositoryAnswersRatherThanFails(t *testing.T) {
	f := gitFixture(t, &fakeGit{status: gitinfo.Status{Repository: false}})
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	output, result := callTool[gitStatusOutput](t, session, "git_status", nil)
	if result != nil && result.IsError {
		t.Fatalf("a plain folder produced a tool error: %+v", result.Content)
	}
	if output.Repository {
		t.Fatal("a plain folder was reported as a repository")
	}
	if output.Dirty {
		t.Error("a plain folder was reported as dirty")
	}
}

func TestGitDiffReturnsThePatchAndHonoursStaged(t *testing.T) {
	git := &fakeGit{diff: gitinfo.Diff{Repository: true, Patch: "diff --git a/a.go b/a.go\n"}}
	f := gitFixture(t, git)
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	output, _ := callTool[gitDiffOutput](t, session, "git_diff", nil)
	if output.Patch == "" || output.Staged {
		t.Fatalf("output = %+v", output)
	}

	staged, _ := callTool[gitDiffOutput](t, session, "git_diff", map[string]any{"staged": true})
	if !staged.Staged {
		t.Fatal("the staged request did not reach the reader")
	}
}

// A path is how a caller would try to read outside the project. The refusal has
// to reach the model as something it can act on.
func TestGitDiffRefusesAnEscapingPathWithAnActionableError(t *testing.T) {
	git := &fakeGit{err: errors.New(`the path must stay inside the repository: "../secrets"`)}
	f := gitFixture(t, git)
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	_, result := callTool[gitDiffOutput](t, session, "git_diff", map[string]any{
		"paths": []string{"../secrets"},
	})
	if result == nil || !result.IsError {
		t.Fatal("an escaping path was accepted")
	}
	message := errorText(result)
	if !contains(message, CodeInvalidPath) {
		t.Fatalf("error = %q, want the typed code %s", message, CodeInvalidPath)
	}
}

// A machine without Git should say so plainly and point at what still works,
// rather than producing a failure the model has to guess at.
func TestAMachineWithoutGitSaysSo(t *testing.T) {
	f := gitFixture(t, &fakeGit{err: gitinfo.ErrGitUnavailable})
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	_, result := callTool[gitStatusOutput](t, session, "git_status", nil)
	if result == nil || !result.IsError {
		t.Fatal("a missing git produced no error")
	}
	message := errorText(result)
	if !contains(message, CodeGitUnavailable) || !contains(message, "exact_search") {
		t.Fatalf("error = %q, want the typed code and what still works", message)
	}
}

// project_info carries the commit-and-dirty half of the freshness contract. It
// is best effort: a project that is not a repository still has a description.
func TestProjectInfoCarriesTheSourceState(t *testing.T) {
	git := &fakeGit{status: gitinfo.Status{
		Repository: true, Branch: "main", Commit: "abc1234def", ShortCommit: "abc1234",
		Dirty: true, Total: 3, Upstream: "origin/main", Ahead: 1,
	}}
	f := gitFixture(t, git)
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	output := callProjectInfo(t, session, nil)
	if output.Source == nil {
		t.Fatal("project_info reported no source state for a repository")
	}
	if output.Source.Branch != "main" || !output.Source.Dirty || output.Source.ChangedFiles != 3 {
		t.Fatalf("source = %+v", output.Source)
	}
	if output.Source.Upstream != "origin/main" || output.Source.Ahead != 1 {
		t.Fatalf("source = %+v, want the upstream position", output.Source)
	}

	found := false
	for _, capability := range output.Capabilities {
		if capability == "git_status" {
			found = true
		}
	}
	if !found {
		t.Fatalf("capabilities = %v, want git_status listed", output.Capabilities)
	}
}

func TestProjectInfoOmitsSourceWhenGitCannotAnswer(t *testing.T) {
	f := gitFixture(t, &fakeGit{err: gitinfo.ErrGitUnavailable})
	session := f.connect(t, "alpha-aaaaaaaa", f.tokens["alpha-aaaaaaaa"])

	output := callProjectInfo(t, session, nil)
	if output.Source != nil {
		t.Fatalf("source = %+v, want it absent when Git cannot answer", output.Source)
	}
	// The rest of the answer still has to be there.
	if output.ProjectID != "alpha-aaaaaaaa" {
		t.Fatalf("project_info stopped answering: %+v", output)
	}
}

// A gateway with no Git reader advertises no Git tools, rather than advertising
// ones that would always fail. The tool list is a contract a model plans
// against, so a tool that cannot work should not be in it.
func TestAGatewayWithoutGitAdvertisesNoGitTools(t *testing.T) {
	// Connected in memory rather than through the fixture, because the fixture
	// always installs a reader and the question here is what happens without one.
	server := New(Options{}).newProjectServer(serveContext{})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, tool := range tools.Tools {
		registered[tool.Name] = true
	}
	for _, name := range []string{"git_status", "git_diff"} {
		if registered[name] {
			t.Errorf("a gateway with no Git reader registered %s", name)
		}
	}
	if !registered["project_info"] {
		t.Error("the gateway stopped serving project_info")
	}
}

func errorText(result *mcp.CallToolResult) string {
	text := ""
	for _, content := range result.Content {
		if block, ok := content.(*mcp.TextContent); ok {
			text += block.Text
		}
	}
	return text
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
