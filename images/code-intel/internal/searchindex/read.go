package searchindex

import (
	"errors"
	"io/fs"
)

// Error codes for reading a named file. They extend the vocabulary in errors.go
// rather than living somewhere else, because the host adapter translates one set.
const (
	// CodeInvalidPath reports a path that is not project-relative. It is a refusal
	// rather than a clamped path: silently reinterpreting a request is worse than
	// declining it.
	CodeInvalidPath = "INVALID_PATH"
	// CodeFileNotFound reports a path that is not a regular file inside the
	// project, or one the project's own ignore rules exclude. The two are one code
	// on purpose -- see ReadProjectFile.
	CodeFileNotFound = "FILE_NOT_FOUND"
	// CodeFileTooLarge reports a file above the caller's byte limit.
	CodeFileTooLarge = "FILE_TOO_LARGE"
)

// Failure exposes the typed parts of an Error without a caller having to know
// which package produced it.
func (e *Error) Failure() (string, string, bool) { return e.Code, e.Message, e.Retryable }

// ReadProjectFile reads one named file that belongs to the project.
//
// "Belongs to the project" is the store's decision and not the caller's, which is
// why this lives here. The store already owns the exclusion policy for the index
// and for the watch set, and a second component deciding what may be read would be
// a second answer to the same question -- with the one that drifted quietly
// handing out files the project excluded.
//
// A path outside the project, a path that is not a regular file, and a path the
// project's ignore rules exclude all produce the same FILE_NOT_FOUND answer. That
// is deliberate: distinguishing them would let a caller map what exists outside
// its scope by reading the difference between two refusals.
func (s *Store) ReadProjectFile(relative string, maxBytes int64) ([]byte, string, error) {
	name, err := normalizeRelative(relative)
	if err != nil {
		return nil, "", fail(CodeInvalidPath, err.Error(), false, nil)
	}

	root, err := s.openWorkspace()
	if err != nil {
		return nil, "", err
	}
	defer root.Close()

	if !s.eligible(root, name) {
		return nil, "", notFound(name)
	}

	info, err := statWithin(root, name)
	if err != nil {
		return nil, "", notFound(name)
	}
	// Not a regular file. A symbolic link is refused rather than followed: the
	// read-only workspace is the boundary this service is trusted to stay inside,
	// and a link is the ordinary way out of one.
	if !info.Mode().IsRegular() {
		return nil, "", notFound(name)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, "", fail(CodeFileTooLarge,
			"The file is larger than this operation will read.", false, nil)
	}

	content, err := readWithin(root, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", notFound(name)
		}
		return nil, "", internal("read "+name, err)
	}
	return content, digestOf(content), nil
}

func notFound(name string) error {
	return fail(CodeFileNotFound,
		"There is no such file in this project: "+name, false, nil)
}
