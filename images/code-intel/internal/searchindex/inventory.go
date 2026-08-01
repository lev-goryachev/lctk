package searchindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// excludedDirectories are skipped whatever the project's ignore rules say.
//
// These hold version-control metadata. Nothing in them is source a caller would
// search for, a repository does not normally list its own version-control
// directory in its ignore file, and no project has a reason to re-include one.
var excludedDirectories = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {},
}

// defaultIgnorePatterns are applied before the project's own rules, so a project
// that says nothing still avoids indexing its dependency and tool caches.
//
// They are ignore patterns rather than hard exclusions precisely so a project
// can overrule them: a "!node_modules/" line in its own ignore file wins,
// because the project knows its layout and LCTK does not.
//
// The list is deliberately short. Names like build, dist, target, and vendor are
// derived output in many projects and real source in others; guessing wrong
// there loses code silently, and a project that treats them as output has
// already said so in its ignore file.
var defaultIgnorePatterns = []string{
	"node_modules/",
	".venv/", "venv/", "__pycache__/", ".mypy_cache/", ".pytest_cache/", ".tox/",
	".gradle/", ".turbo/", ".next/", ".nuxt/", ".parcel-cache/",
}

type inventoryResult struct {
	files      map[string]string
	skippedBig int
	// skippedIgnored counts entries excluded by the project's own ignore rules,
	// so the effect is reportable rather than invisible.
	skippedIgnored int
}

// inventory walks the workspace and records a digest per eligible file.
//
// Enumeration is LCTK's, not Git's. That is a requirement rather than a
// convenience: a file the user has saved but not committed, or not even added,
// is exactly the file an agent is most likely to be asking about.
func (s *Store) inventory(ctx context.Context) (inventoryResult, error) {
	result := inventoryResult{files: map[string]string{}}

	// Ignore rules are collected per directory as the walk descends, because a
	// nested ignore file adds rules for its own subtree only.
	rules := map[string]ignoreSet{"": rootIgnoreSet(s.Workspace)}

	err := filepath.WalkDir(s.Workspace, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == s.Workspace {
			return nil
		}

		relative, err := filepath.Rel(s.Workspace, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		parent := path.Dir(relative)
		if parent == "." {
			parent = ""
		}
		inherited := rules[parent]

		if entry.IsDir() {
			if _, excluded := excludedDirectories[entry.Name()]; excluded {
				return filepath.SkipDir
			}
			if inherited.ignored(relative, true) {
				result.skippedIgnored++
				return filepath.SkipDir
			}
			rules[relative] = inherited.withFile(name, relative)
			return nil
		}

		// Only regular files. A symlink is skipped rather than followed, because
		// following one is the classic way out of a mount, and the read-only
		// workspace is the boundary this service is trusted to stay inside.
		if !entry.Type().IsRegular() {
			return nil
		}
		if inherited.ignored(relative, false) {
			result.skippedIgnored++
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > s.Limits.MaxFileBytes {
			result.skippedBig++
			return nil
		}

		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		result.files[relative] = digestOf(content)
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inventoryResult{}, ctxErr
		}
		return inventoryResult{}, internal("enumerate the workspace", err)
	}
	return result, nil
}

// eligible reports whether a single path would be indexed, applying the same
// ignore rules the walk applies.
//
// A targeted update has to agree with a full build, or a change to an ignored
// file would add it to the index and the next rebuild would silently drop it
// again.
func (s *Store) eligible(relative string) bool {
	segments := strings.Split(relative, "/")
	rules := rootIgnoreSet(s.Workspace)
	prefix := ""

	for depth, segment := range segments {
		if _, excluded := excludedDirectories[segment]; excluded {
			return false
		}
		if prefix == "" {
			prefix = segment
		} else {
			prefix = prefix + "/" + segment
		}
		isDir := depth < len(segments)-1
		if rules.ignored(prefix, isDir) {
			return false
		}
		if isDir {
			rules = rules.withFile(filepath.Join(s.Workspace, filepath.FromSlash(prefix)), prefix)
		}
	}
	return true
}

// digestFile returns the digest and size of one workspace-relative file.
func (s *Store) digestFile(relative string) (string, int64, error) {
	absolute := filepath.Join(s.Workspace, filepath.FromSlash(relative))
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fs.ErrNotExist
	}
	if info.Size() > s.Limits.MaxFileBytes {
		return "", info.Size(), nil
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return "", 0, err
	}
	return digestOf(content), info.Size(), nil
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// normalizeRelative rejects anything that is not a project-relative path.
//
// This is a scope boundary, not input tidying. An absolute path or a parent
// traversal is precisely how a caller would try to reach outside the project,
// and the answer is a refusal rather than a clamped path, because silently
// reinterpreting a request is worse than declining it.
func normalizeRelative(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("the path is empty")
	}
	// Windows-style separators and drive letters are rejected before cleaning:
	// on Linux, filepath.Clean would treat "C:\x" as one ordinary file name.
	if strings.ContainsAny(trimmed, "\\") || looksAbsoluteWindows(trimmed) {
		return "", fmt.Errorf("the path must be project-relative and use forward slashes: %q", name)
	}
	cleaned := path.Clean(filepath.ToSlash(trimmed))
	switch {
	case path.IsAbs(cleaned):
		return "", fmt.Errorf("the path must be project-relative, not absolute: %q", name)
	case cleaned == "." || cleaned == "..":
		return "", fmt.Errorf("the path must name a file: %q", name)
	case cleaned == ".." || strings.HasPrefix(cleaned, "../"):
		return "", fmt.Errorf("the path must stay inside the project: %q", name)
	}
	return cleaned, nil
}

func looksAbsoluteWindows(name string) bool {
	if len(name) < 2 {
		return false
	}
	c := name[0]
	isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	return isLetter && name[1] == ':'
}
