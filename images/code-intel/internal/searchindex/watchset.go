package searchindex

import (
	"context"
	"io/fs"
	"path"
)

// MaxWatchDirectories bounds the watch set the service will describe.
//
// The list exists so a host watcher can register one native watch per directory.
// Past some size that stops being a useful answer: the host would exhaust
// descriptors or handles, and the honest response is "watch nothing, reconcile
// instead" rather than a list that cannot be acted on. The bound is a variable so
// a test can drive the same behaviour at small scale.
var MaxWatchDirectories = 20000

// WatchSet lists the project-relative directories a host watcher must observe in
// order to see every change that could reach the index.
//
// It is served by the service rather than computed on the host because the
// service already owns the exclusion policy. Two implementations of "what belongs
// to this project" would drift, and the one that drifted would be the watcher,
// silently missing edits in a directory the indexer cares about.
//
// The root is reported as "." so every element is a valid relative path.
// Truncated reports that the project has more directories than the bound, which
// the caller must treat as "this set is incomplete" rather than as the whole
// project.
func (s *Store) WatchSet(ctx context.Context) (directories []string, truncated bool, err error) {
	root, err := s.openWorkspace()
	if err != nil {
		return nil, false, err
	}
	defer root.Close()

	directories = []string{"."}
	rules := map[string]ignoreSet{"": rootIgnoreSet(root, nil)}

	walkErr := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." || !entry.IsDir() {
			return nil
		}
		if _, excluded := excludedDirectories[entry.Name()]; excluded {
			return fs.SkipDir
		}

		parent := path.Dir(name)
		if parent == "." {
			parent = ""
		}
		inherited := rules[parent]
		if inherited.ignored(name, true) {
			return fs.SkipDir
		}
		rules[name] = inherited.withFiles(root, name, nil)

		if len(directories) >= MaxWatchDirectories {
			truncated = true
			// Stop walking rather than keep counting. The answer is already
			// "too many", and finishing the walk would only make it slower.
			return fs.SkipAll
		}
		directories = append(directories, name)
		return nil
	})
	if walkErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, false, internal("enumerate the project's directories", walkErr)
	}
	return directories, truncated, nil
}
