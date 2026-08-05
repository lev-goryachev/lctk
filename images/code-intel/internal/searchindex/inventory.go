package searchindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	files map[string]string
	// sourceBytes is the total size of the indexed files. It is what makes an
	// index-size estimate possible for a project that has never been built: the
	// only useful predictor of index size is how much source there is.
	sourceBytes int64
	skippedBig  int
	// skippedIgnored counts entries excluded by the project's own ignore rules,
	// so the effect is reportable rather than invisible.
	skippedIgnored int
	// ignoreSources names the ignore files that were actually found, so an
	// operator can see which rules were in effect instead of inferring it from
	// what went missing.
	ignoreSources []string
}

// inventory walks the workspace and records a digest per eligible file.
//
// Enumeration is LCTK's, not Git's. That is a requirement rather than a
// convenience: a file the user has saved but not committed, or not even added,
// is exactly the file an agent is most likely to be asking about.
func (s *Store) inventory(ctx context.Context) (inventoryResult, error) {
	root, err := s.openWorkspace()
	if err != nil {
		return inventoryResult{}, err
	}
	defer root.Close()

	result := inventoryResult{files: map[string]string{}}
	type fileCandidate struct {
		name string
		size int64
	}
	var candidates []fileCandidate
	sources := &sourceSet{}

	// Ignore rules are collected per directory as the walk descends, because a
	// nested ignore file adds rules for its own subtree only.
	rules := map[string]ignoreSet{"": rootIgnoreSet(root, sources)}

	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}

		parent := path.Dir(name)
		if parent == "." {
			parent = ""
		}
		inherited := rules[parent]

		if entry.IsDir() {
			if _, excluded := excludedDirectories[entry.Name()]; excluded {
				return fs.SkipDir
			}
			if inherited.ignored(name, true) {
				result.skippedIgnored++
				return fs.SkipDir
			}
			rules[name] = inherited.withFiles(root, name, sources)
			return nil
		}

		// Only regular files. A symlink is skipped rather than followed: the
		// read-only workspace is the boundary this service is trusted to stay
		// inside, and a link is the ordinary way out of one.
		if !entry.Type().IsRegular() {
			return nil
		}
		// An ignore file is never itself ignored. It has to appear in the
		// inventory, or a change to it goes unnoticed and the rules it declares
		// silently stop matching what is actually indexed. Excluding a file that
		// decides exclusions is a rule that hides its own edits.
		if !isIgnoreFile(name) && inherited.ignored(name, false) {
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

		candidates = append(candidates, fileCandidate{name: name, size: info.Size()})
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inventoryResult{}, ctxErr
		}
		return inventoryResult{}, internal("enumerate the workspace", err)
	}

	// Directory descent and ignore inheritance are serial. Once that decision is
	// complete, file reads are independent and bounded parallelism avoids paying
	// one storage round trip at a time on a million-file repository.
	workers := s.Limits.Parallelism
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = min(workers, len(candidates))
	digests := make([]string, len(candidates))
	var next atomic.Int64
	var firstErr error
	var errorOnce sync.Once
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				index := int(next.Add(1) - 1)
				if index >= len(candidates) {
					return
				}
				if err := ctx.Err(); err != nil {
					errorOnce.Do(func() { firstErr = err })
					return
				}
				content, err := readWithin(root, candidates[index].name)
				if err != nil {
					errorOnce.Do(func() { firstErr = err })
					return
				}
				digests[index] = digestOf(content)
			}
		}()
	}
	wait.Wait()
	if firstErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inventoryResult{}, ctxErr
		}
		return inventoryResult{}, internal("read the workspace inventory", firstErr)
	}
	for index, candidate := range candidates {
		result.files[candidate.name] = digests[index]
		result.sourceBytes += candidate.size
	}
	result.ignoreSources = sources.list()
	return result, nil
}

// eligible reports whether a single path would be indexed, applying the same
// ignore rules the walk applies.
//
// A targeted update has to agree with a full build, or a change to an ignored
// file would add it to the index and the next rebuild would silently drop it
// again.
func (s *Store) eligible(root *os.Root, relative string) bool {
	segments := strings.Split(relative, "/")
	rules := rootIgnoreSet(root, nil)
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
		if (isDir || !isIgnoreFile(prefix)) && rules.ignored(prefix, isDir) {
			return false
		}
		if isDir {
			rules = rules.withFiles(root, prefix, nil)
		}
	}
	return true
}

// digestFile returns the digest and size of one workspace-relative file.
func digestFile(root *os.Root, relative string, maxBytes int64) (string, int64, error) {
	info, err := statWithin(root, relative)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fs.ErrNotExist
	}
	if info.Size() > maxBytes {
		return "", info.Size(), nil
	}
	content, err := readWithin(root, relative)
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
	// on Linux, path.Clean would treat "C:\x" as one ordinary file name.
	if strings.ContainsAny(trimmed, `\`) || looksAbsoluteWindows(trimmed) {
		return "", fmt.Errorf("the path must be project-relative and use forward slashes: %q", name)
	}
	cleaned := path.Clean(trimmed)
	switch {
	case path.IsAbs(cleaned):
		return "", fmt.Errorf("the path must be project-relative, not absolute: %q", name)
	case cleaned == "." || cleaned == "..":
		return "", fmt.Errorf("the path must name a file: %q", name)
	case strings.HasPrefix(cleaned, "../"):
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
