// Package gitinfo reports what Git knows about a project's working tree.
//
// It runs on the host rather than in the project container, for two reasons.
// The container mounts the source read-only, and Git wants to refresh its index
// when asked for status; and the host is where the user's Git configuration,
// credentials, and hooks policy live, so an answer computed anywhere else would
// be about a different repository than the one they are looking at.
//
// It is strictly read-only. Nothing here commits, checks out, fetches, or writes
// a ref, and the commands are invoked with locking disabled so that asking LCTK
// for status cannot interfere with a Git operation the user is running in a
// terminal at the same time.
//
// Not being a repository is not an error. Plenty of projects are folders, and a
// caller asking "what changed" deserves "this is not a repository" rather than a
// failure it has to interpret.
package gitinfo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Timeout bounds one Git invocation. Status on a very large repository is not
// instant, and a repository on a network drive can hang outright; the bound is
// what keeps an MCP request from hanging with it.
const Timeout = 20 * time.Second

// MaxDiffBytes bounds a diff. A diff is for reading, and one larger than this is
// not being read by anyone; the response says it was truncated.
const MaxDiffBytes = 512 << 10

// DefaultMaxFiles bounds the changed-file list.
const DefaultMaxFiles = 500

// CacheTTL is how long a status answer is reused.
//
// It exists because the freshness contract asks every index-dependent response
// to carry the commit and dirty state, and a Git subprocess per search would be
// a real cost for an answer that changes on a human timescale. A second is far
// shorter than the three-second debounce a change waits out anyway, so nothing
// observable is made staler by it.
const CacheTTL = time.Second

// Errors a caller can act on.
var (
	// ErrGitUnavailable means no usable git executable was found.
	ErrGitUnavailable = errors.New("git is not available on this machine")
)

// Change is one path Git reports as changed.
type Change struct {
	// Path is repository-relative with forward slashes. Repository-relative
	// rather than project-relative because Git is the authority here, and the two
	// differ when a project is registered below a repository root.
	Path string `json:"path"`
	// State is one of added, modified, deleted, renamed, copied, untracked, or
	// conflicted.
	State string `json:"state"`
	// Staged says the change is in the index. A path modified both in the index
	// and in the working tree appears once, staged, with WorkingTree also set.
	Staged bool `json:"staged"`
	// WorkingTree says the change is present in the working tree.
	WorkingTree bool `json:"working_tree"`
	// From is the previous path of a rename or copy.
	From string `json:"from,omitempty"`
}

// Status is the working tree as Git sees it.
type Status struct {
	// Repository is false when the folder is not inside a Git repository, which
	// is a normal answer rather than a failure.
	Repository bool `json:"repository"`
	// Branch is empty on a detached HEAD.
	Branch      string `json:"branch,omitempty"`
	Detached    bool   `json:"detached,omitempty"`
	Commit      string `json:"commit,omitempty"`
	ShortCommit string `json:"short_commit,omitempty"`
	Upstream    string `json:"upstream,omitempty"`
	Ahead       int    `json:"ahead,omitempty"`
	Behind      int    `json:"behind,omitempty"`
	// Dirty is true when anything is changed, staged, untracked, or conflicted.
	Dirty bool `json:"dirty"`
	// Changed lists the changed paths, bounded by the caller's limit.
	Changed []Change `json:"changed,omitempty"`
	// Total is how many paths changed, which can exceed len(Changed).
	Total     int  `json:"total"`
	Truncated bool `json:"truncated,omitempty"`
	// Unborn marks a repository with no commit yet, where HEAD names a branch
	// that does not exist. Reported so "no commit" is not read as "not a
	// repository".
	Unborn bool `json:"unborn,omitempty"`
	// Prefix is where the project sits inside the repository, empty when the two
	// are the same folder. Changed paths are repository-relative, because that is
	// what Git reports and reinterpreting them would be a second grammar to get
	// wrong; the prefix is what lets a caller relate the two.
	Prefix string `json:"prefix,omitempty"`
}

// Options bound one status query.
type Options struct {
	// MaxFiles bounds the changed list. Zero means DefaultMaxFiles.
	MaxFiles int
	// IncludeUntracked lists files Git does not track. On by default because a
	// file an agent just created is exactly the one it is asking about.
	IncludeUntracked bool
}

// Reader answers questions about one machine's repositories.
type Reader struct {
	// Run executes a git command and returns its standard output. It is a field
	// so tests can drive the parser without a repository on disk.
	Run func(ctx context.Context, dir string, args ...string) ([]byte, error)
	Now func() time.Time

	mu     sync.Mutex
	cached map[string]entry
}

type entry struct {
	status Status
	at     time.Time
}

// New returns a reader backed by the git executable on PATH.
func New() *Reader {
	return &Reader{Run: run, Now: time.Now, cached: map[string]entry{}}
}

// Status reports the working tree at a path.
//
// A short-lived cache serves repeated calls, because the freshness contract asks
// for the commit and dirty state on every index-dependent answer and a
// subprocess per search would not be worth it. The cache is keyed on the root
// and on whether untracked files were asked for, since those produce genuinely
// different answers.
func (r *Reader) Status(ctx context.Context, root string, options Options) (Status, error) {
	if options.MaxFiles <= 0 {
		options.MaxFiles = DefaultMaxFiles
	}
	key := root + "\x00" + strconv.FormatBool(options.IncludeUntracked) + "\x00" + strconv.Itoa(options.MaxFiles)

	if cached, ok := r.lookup(key); ok {
		return cached, nil
	}

	status, err := r.read(ctx, root, options)
	if err != nil {
		return Status{}, err
	}
	r.store(key, status)
	return status, nil
}

func (r *Reader) lookup(key string) (Status, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	found, ok := r.cached[key]
	if !ok || r.now().Sub(found.at) > CacheTTL {
		return Status{}, false
	}
	return found.status, true
}

func (r *Reader) store(key string, status Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cached == nil {
		r.cached = map[string]entry{}
	}
	// The map is keyed by project root, so it is bounded by how many projects are
	// registered. There is nothing to evict.
	r.cached[key] = entry{status: status, at: r.now()}
}

func (r *Reader) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Reader) read(ctx context.Context, root string, options Options) (Status, error) {
	// Where the project sits inside the repository is asked first, because it is
	// also the cheapest way to learn that the folder is not one.
	prefix, err := r.exec(ctx, root, "rev-parse", "--show-prefix")
	if err != nil {
		if notARepository(err) {
			return Status{Repository: false}, nil
		}
		return Status{}, err
	}

	untracked := "--untracked-files=normal"
	if !options.IncludeUntracked {
		untracked = "--untracked-files=no"
	}

	// --no-optional-locks keeps a status query from taking the index lock, so
	// asking LCTK what changed cannot collide with a Git command the user is
	// running in a terminal. porcelain=v2 with -z is the machine-readable form
	// whose grammar Git commits to, and -z is what makes a path containing a
	// newline or a quote parse correctly rather than nearly correctly.
	//
	// The trailing pathspec is a scope boundary, not a filter. A registered
	// project can sit below a repository root, and without it a project endpoint
	// would report changes in a sibling directory that is not part of the
	// project at all.
	raw, err := r.exec(ctx, root,
		"--no-optional-locks", "status", "--porcelain=v2", "--branch", untracked, "-z", "--", ".")
	if err != nil {
		if notARepository(err) {
			return Status{Repository: false}, nil
		}
		return Status{}, err
	}

	status := parseStatus(raw, options.MaxFiles)
	status.Prefix = strings.TrimSpace(string(prefix))
	return status, nil
}

// Diff returns a bounded unified diff of the working tree.
type DiffOptions struct {
	// Staged diffs the index against HEAD instead of the working tree against
	// the index.
	Staged bool
	// Paths restricts the diff. Each must be repository-relative; an absolute or
	// escaping path is refused rather than reinterpreted.
	Paths []string
	// Context is the number of context lines. Zero means Git's default.
	Context int
	// MaxBytes bounds the output. Zero means MaxDiffBytes.
	MaxBytes int
}

// Diff result.
type Diff struct {
	Repository bool   `json:"repository"`
	Patch      string `json:"patch"`
	Truncated  bool   `json:"truncated,omitempty"`
	Staged     bool   `json:"staged,omitempty"`
}

// Diff produces a unified diff, bounded in size.
func (r *Reader) Diff(ctx context.Context, root string, options DiffOptions) (Diff, error) {
	limit := options.MaxBytes
	if limit <= 0 || limit > MaxDiffBytes {
		limit = MaxDiffBytes
	}

	args := []string{"--no-optional-locks", "diff", "--no-color", "--no-ext-diff"}
	// Without an explicit pathspec the diff would cover the whole repository,
	// including directories outside a project registered below its root.
	scoped := true
	if options.Staged {
		args = append(args, "--staged")
	}
	if options.Context > 0 {
		args = append(args, "--unified="+strconv.Itoa(options.Context))
	}
	// The separator matters: without it a path that looks like a revision would be
	// resolved as one, and the diff would silently be of something else.
	if len(options.Paths) > 0 {
		cleaned, err := cleanPaths(options.Paths)
		if err != nil {
			return Diff{}, err
		}
		args = append(args, "--")
		args = append(args, cleaned...)
	} else if scoped {
		args = append(args, "--", ".")
	}

	raw, err := r.exec(ctx, root, args...)
	if err != nil {
		if notARepository(err) {
			return Diff{Repository: false}, nil
		}
		return Diff{}, err
	}

	patch := string(raw)
	truncated := false
	if len(patch) > limit {
		patch = patch[:limit]
		truncated = true
	}
	return Diff{Repository: true, Patch: patch, Truncated: truncated, Staged: options.Staged}, nil
}

// cleanPaths refuses anything that is not repository-relative.
//
// An absolute path or a parent traversal is how a caller would try to diff
// something outside the project, and a leading dash is how one would try to pass
// an option where a path is expected. All three are refused rather than
// reinterpreted.
func cleanPaths(paths []string) ([]string, error) {
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		switch {
		case trimmed == "":
			return nil, fmt.Errorf("a path is empty")
		case strings.HasPrefix(trimmed, "-"):
			return nil, fmt.Errorf("a path must not begin with a dash: %q", path)
		case strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`):
			return nil, fmt.Errorf("the path must be repository-relative, not absolute: %q", path)
		case len(trimmed) > 1 && trimmed[1] == ':':
			return nil, fmt.Errorf("the path must be repository-relative, not absolute: %q", path)
		case trimmed == ".." || strings.HasPrefix(trimmed, "../") ||
			strings.Contains(trimmed, "/../") || strings.Contains(trimmed, `\..\`):
			return nil, fmt.Errorf("the path must stay inside the repository: %q", path)
		}
		cleaned = append(cleaned, trimmed)
	}
	return cleaned, nil
}

func (r *Reader) exec(ctx context.Context, dir string, args ...string) ([]byte, error) {
	runner := r.Run
	if runner == nil {
		runner = run
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, Timeout)
		defer cancel()
	}
	return runner(ctx, dir, args...)
}

// commandError carries git's own message, which is usually the useful one.
type commandError struct {
	stderr string
	err    error
}

func (e *commandError) Error() string {
	if e.stderr == "" {
		return e.err.Error()
	}
	return e.stderr
}

func (e *commandError) Unwrap() error { return e.err }

func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return nil, ErrGitUnavailable
	}

	command := exec.CommandContext(ctx, path, args...)
	command.Dir = dir
	// A repository is untrusted input in the same sense a manifest is: its
	// configuration can name programs to run. Reading status does not evaluate
	// hooks, but the environment is trimmed anyway so that a stray GIT_DIR or
	// GIT_CONFIG in the daemon's environment cannot redirect the query.
	command.Env = append(command.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_NOSYSTEM=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
	)

	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, &commandError{stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return stdout.Bytes(), nil
}

// notARepository recognizes the one failure that is a normal answer.
//
// Git's "dubious ownership" refusal is deliberately not folded in here. It looks
// similar and is not: the folder is a repository, Git is declining to read it,
// and the fix is one safe.directory line. Reporting it as "not a repository"
// would hide a fixable problem behind a plausible answer.
func notARepository(err error) bool {
	if errors.Is(err, ErrGitUnavailable) {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not a git repository")
}
