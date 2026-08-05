//go:build !windows

package containerruntime

import (
	"errors"
	"fmt"
	"path/filepath"
)

// ErrUnsupportedHostPath reports a path that cannot be mounted by the runtime.
var ErrUnsupportedHostPath = errors.New("the managed runtime requires an absolute host path")

// HostPath returns the native absolute path on Unix hosts.
func HostPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %q is not absolute", ErrUnsupportedHostPath, path)
	}
	return filepath.Clean(path), nil
}
