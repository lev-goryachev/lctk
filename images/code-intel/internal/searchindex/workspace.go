package searchindex

import (
	"io/fs"
	"os"
)

// openWorkspace opens the project source as a traversal root.
//
// Every read of project content goes through [os.Root] rather than through a
// path joined onto the workspace directory. The difference is not stylistic. A
// joined path is checked once and opened later, so a symbolic link created
// between those two moments escapes the mount; a root refuses to leave its own
// tree at open time, enforced by the operating system where it can be. The
// service's entire job is to stay inside one read-only mount, so that guarantee
// belongs at the point of the read and not in a validation function some future
// caller might bypass.
func (s *Store) openWorkspace() (*os.Root, error) {
	root, err := os.OpenRoot(s.Workspace)
	if err != nil {
		return nil, internal("open the project workspace", err)
	}
	return root, nil
}

// readWithin reads a project-relative file through the root.
func readWithin(root *os.Root, relative string) ([]byte, error) {
	return root.ReadFile(relative)
}

// statWithin stats a project-relative entry without following a final symlink.
func statWithin(root *os.Root, relative string) (fs.FileInfo, error) {
	return root.Lstat(relative)
}
