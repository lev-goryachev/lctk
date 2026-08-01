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

// excludedDirectories are skipped wholesale during enumeration.
//
// These are directories whose contents are derived, vendored, or private to a
// tool. Indexing them costs space and answers questions about generated output
// rather than about the project.
var excludedDirectories = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {},
	"node_modules": {}, "vendor": {}, ".venv": {},
	"dist": {}, "build": {}, "target": {}, "coverage": {},
	".idea": {}, ".vscode": {}, ".gradle": {}, "__pycache__": {},
}

type inventoryResult struct {
	files      map[string]string
	skippedBig int
}

// inventory walks the workspace and records a digest per eligible file.
//
// Enumeration is LCTK's, not Git's. That is a requirement rather than a
// convenience: a file the user has saved but not committed, or not even added,
// is exactly the file an agent is most likely to be asking about.
func (s *Store) inventory(ctx context.Context) (inventoryResult, error) {
	result := inventoryResult{files: map[string]string{}}

	err := filepath.WalkDir(s.Workspace, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if name == s.Workspace {
				return nil
			}
			if _, excluded := excludedDirectories[entry.Name()]; excluded {
				return filepath.SkipDir
			}
			return nil
		}
		// Only regular files. A symlink is skipped rather than followed, because
		// following one is the classic way out of a mount, and the read-only
		// workspace is the boundary this service is trusted to stay inside.
		if !entry.Type().IsRegular() {
			return nil
		}

		relative, err := filepath.Rel(s.Workspace, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)

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
