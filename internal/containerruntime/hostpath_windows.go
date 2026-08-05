package containerruntime

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnsupportedHostPath reports a Windows path the managed WSL machine cannot
// mount without a separately accepted sharing contract.
var ErrUnsupportedHostPath = errors.New("the managed WSL runtime supports local drive paths only")

// HostPath translates an authoritative native Windows project path to the
// corresponding WSL automount path visible to the Podman service.
func HostPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %q is not absolute", ErrUnsupportedHostPath, path)
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return "", fmt.Errorf("%w: %q is not on a local drive", ErrUnsupportedHostPath, path)
	}
	drive := strings.ToLower(volume[:1])
	remainder := strings.TrimPrefix(path, volume)
	remainder = strings.ReplaceAll(remainder, `\`, "/")
	remainder = strings.TrimLeft(remainder, "/")
	if remainder == "" {
		return "/mnt/" + drive, nil
	}
	return "/mnt/" + drive + "/" + remainder, nil
}
