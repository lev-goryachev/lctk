package gateway

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lev-goryachev/lctk/internal/gitinfo"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

// Typed codes the Git tools report. They are the gateway's vocabulary, chosen so
// a caller can tell "there is nothing to answer" from "the answer failed".
// A folder that is not a repository is not among them: that is reported as
// repository:false in the answer, because it is an answer.
const (
	CodeGitUnavailable = "GIT_UNAVAILABLE"
	CodeGitFailed      = "GIT_FAILED"
	CodeInvalidPath    = "INVALID_PATH"
)

// gitStatusInput is the public request schema.
//
// It declares the scope-like fields for the same reason the other tools do: a
// model will supply them, and saying "this is ignored" beats leaving it to guess.
type gitStatusInput struct {
	IncludeUntracked *bool `json:"include_untracked,omitempty" jsonschema:"List files Git does not track. Defaults to true."`
	Limit            int   `json:"limit,omitempty" jsonschema:"Maximum changed paths to return. Defaults to 500."`

	ProjectID      string `json:"project_id,omitempty" jsonschema:"Ignored. The authoritative project comes from the endpoint."`
	RepositoryRoot string `json:"repository_root,omitempty" jsonschema:"Ignored. The authoritative root comes from the registry."`
	Path           string `json:"path,omitempty" jsonschema:"Ignored. This tool reports the whole project."`
}

type gitStatusOutput struct {
	ProjectID string `json:"project_id"`
	// ScopeSource states where the answer came from, so a caller can verify that
	// its own arguments did not influence it.
	ScopeSource string `json:"scope_source"`
	Root        string `json:"root"`
	// Repository is false when the project folder is not in a Git repository,
	// which is an answer rather than a failure.
	Repository bool   `json:"repository"`
	Branch     string `json:"branch,omitempty"`
	Detached   bool   `json:"detached,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Upstream   string `json:"upstream,omitempty"`
	Ahead      int    `json:"ahead,omitempty"`
	Behind     int    `json:"behind,omitempty"`
	Unborn     bool   `json:"unborn,omitempty"`
	Dirty      bool   `json:"dirty"`
	// Prefix is where the project sits inside the repository. Changed paths are
	// repository-relative, and this is what relates them to the project.
	Prefix    string           `json:"prefix,omitempty"`
	Changed   []gitinfo.Change `json:"changed"`
	Total     int              `json:"total"`
	Truncated bool             `json:"truncated,omitempty"`
}

type gitDiffInput struct {
	Staged   bool     `json:"staged,omitempty" jsonschema:"Diff what is staged against the last commit instead of the working tree against the index."`
	Paths    []string `json:"paths,omitempty" jsonschema:"Repository-relative paths to restrict the diff to. An absolute or escaping path is refused."`
	Context  int      `json:"context,omitempty" jsonschema:"Context lines around each change. Defaults to Git's own."`
	MaxBytes int      `json:"max_bytes,omitempty" jsonschema:"Maximum patch size. Defaults to 524288, which is also the ceiling."`

	ProjectID      string `json:"project_id,omitempty" jsonschema:"Ignored. The authoritative project comes from the endpoint."`
	RepositoryRoot string `json:"repository_root,omitempty" jsonschema:"Ignored. The authoritative root comes from the registry."`
}

type gitDiffOutput struct {
	ProjectID   string `json:"project_id"`
	ScopeSource string `json:"scope_source"`
	Root        string `json:"root"`
	Repository  bool   `json:"repository"`
	Staged      bool   `json:"staged,omitempty"`
	Patch       string `json:"patch"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// registerGitTools adds the working-tree tools to a project's server.
//
// They exist because an agent talking to LCTK over HTTP has no shell on the
// user's machine. That is the value LCTK adds here rather than duplicating: an
// editor's own terminal can already run git, and this is for the caller that
// cannot. The scope is the route's, so it can only ever be this project.
func (g *Gateway) registerGitTools(server *mcp.Server, resolved serveContext) {
	if g.options.Git == nil {
		return
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "git_status",
		Description: "Report the project's Git branch, commit, and changed files. " +
			"Read-only: nothing is committed, checked out, or fetched. " +
			"Paths are repository-relative, and prefix says where the project sits inside the repository. " +
			"The scope comes from the endpoint; arguments naming a project or path are ignored.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input gitStatusInput) (*mcp.CallToolResult, gitStatusOutput, error) {
		untracked := true
		if input.IncludeUntracked != nil {
			untracked = *input.IncludeUntracked
		}

		status, err := g.options.Git.Status(ctx, resolved.project.Path, gitinfo.Options{
			MaxFiles:         input.Limit,
			IncludeUntracked: untracked,
		})
		if err != nil {
			return nil, gitStatusOutput{}, asGitToolError(err)
		}

		output := gitStatusOutput{
			ProjectID:   resolved.project.ID,
			ScopeSource: "route_and_registry",
			Root:        projectstack.WorkspaceMount,
			Repository:  status.Repository,
			Branch:      status.Branch,
			Detached:    status.Detached,
			Commit:      status.Commit,
			Upstream:    status.Upstream,
			Ahead:       status.Ahead,
			Behind:      status.Behind,
			Unborn:      status.Unborn,
			Dirty:       status.Dirty,
			Prefix:      status.Prefix,
			Changed:     status.Changed,
			Total:       status.Total,
			Truncated:   status.Truncated,
		}
		if output.Changed == nil {
			output.Changed = []gitinfo.Change{}
		}
		return nil, output, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "git_diff",
		Description: "Return a unified diff of the project's uncommitted changes. " +
			"Read-only and bounded in size; the response says when it was truncated. " +
			"The scope comes from the endpoint, and a path outside the repository is refused.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input gitDiffInput) (*mcp.CallToolResult, gitDiffOutput, error) {
		diff, err := g.options.Git.Diff(ctx, resolved.project.Path, gitinfo.DiffOptions{
			Staged:   input.Staged,
			Paths:    input.Paths,
			Context:  input.Context,
			MaxBytes: input.MaxBytes,
		})
		if err != nil {
			return nil, gitDiffOutput{}, asGitToolError(err)
		}
		return nil, gitDiffOutput{
			ProjectID:   resolved.project.ID,
			ScopeSource: "route_and_registry",
			Root:        projectstack.WorkspaceMount,
			Repository:  diff.Repository,
			Staged:      diff.Staged,
			Patch:       diff.Patch,
			Truncated:   diff.Truncated,
		}, nil
	})
}

// sourceOf reads the working tree for project_info, best effort.
//
// A failure here is not a failure of project_info. A machine without Git, or a
// folder that is not a repository, still has a project worth describing, and the
// absent block says so more clearly than an error would.
func (g *Gateway) sourceOf(ctx context.Context, resolved serveContext) *sourceInfo {
	if g.options.Git == nil {
		return nil
	}
	// The changed list is not wanted here, only its size, so the smallest limit
	// that still counts everything is used.
	status, err := g.options.Git.Status(ctx, resolved.project.Path, gitinfo.Options{
		MaxFiles:         1,
		IncludeUntracked: true,
	})
	if err != nil || !status.Repository {
		return nil
	}
	return &sourceInfo{
		Branch:       status.Branch,
		Detached:     status.Detached,
		Commit:       status.Commit,
		ShortCommit:  status.ShortCommit,
		Upstream:     status.Upstream,
		Ahead:        status.Ahead,
		Behind:       status.Behind,
		Dirty:        status.Dirty,
		ChangedFiles: status.Total,
		Unborn:       status.Unborn,
		Prefix:       status.Prefix,
	}
}

// asGitToolError turns a failure into something a model can act on.
func asGitToolError(err error) error {
	switch {
	case errors.Is(err, gitinfo.ErrGitUnavailable):
		return &searchToolError{
			code:    CodeGitUnavailable,
			message: "Git is not available on this machine.",
			action:  "Install Git, or use exact_search, which does not need it.",
		}
	case isInvalidPath(err):
		return &searchToolError{
			code:    CodeInvalidPath,
			message: err.Error(),
			action:  "Use a repository-relative path.",
		}
	default:
		return &searchToolError{
			code:      CodeGitFailed,
			message:   "Git could not answer: " + firstLine(err.Error()),
			retryable: true,
			action:    "Check the repository with git status in a terminal.",
		}
	}
}

// isInvalidPath recognizes the refusals cleanPaths produces, which are the
// caller's fault rather than the repository's.
func isInvalidPath(err error) bool {
	message := err.Error()
	return strings.Contains(message, "repository-relative") ||
		strings.Contains(message, "stay inside the repository") ||
		strings.Contains(message, "must not begin with a dash") ||
		strings.Contains(message, "a path is empty")
}

// firstLine keeps a tool error to one line. Git's diagnostics can run to several,
// and the first is the one that says what happened.
func firstLine(message string) string {
	if index := strings.IndexAny(message, "\r\n"); index >= 0 {
		return strings.TrimSpace(message[:index])
	}
	return strings.TrimSpace(message)
}
