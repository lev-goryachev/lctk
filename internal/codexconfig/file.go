package codexconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// defaultMode is used when LCTK creates the configuration file itself. The
// document holds no secret, only the name of the variable that does.
const defaultMode fs.FileMode = 0o644

// ReadFile returns the document, and an empty string when the file does not
// exist. A missing Codex configuration is a normal starting state, not an error.
func ReadFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read Codex configuration %q: %w", path, err)
	}
	return string(raw), nil
}

// WriteFile replaces the document atomically, after copying the previous
// contents to a backup beside it. It returns the backup path, empty when there
// was nothing to back up.
//
// The backup is not a convenience. LCTK writes into a file it does not own and
// that other tools also write, so a recoverable previous version is part of
// being allowed to write at all.
func WriteFile(path, content string) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create Codex home %q: %w", dir, err)
	}

	mode := defaultMode
	backup := ""
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		previous, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read Codex configuration %q before writing: %w", path, err)
		}
		backup = path + BackupSuffix
		if err := os.WriteFile(backup, previous, mode); err != nil {
			return "", fmt.Errorf("write backup %q: %w", backup, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect Codex configuration %q: %w", path, err)
	}

	temp, err := os.CreateTemp(dir, FileName+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("create a temporary file in %q: %w", dir, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return "", fmt.Errorf("write temporary Codex configuration: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", fmt.Errorf("flush temporary Codex configuration: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close temporary Codex configuration: %w", err)
	}
	if err := os.Chmod(tempName, mode); err != nil {
		return "", fmt.Errorf("set permissions on the temporary Codex configuration: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return "", fmt.Errorf("replace Codex configuration %q: %w", path, err)
	}
	return backup, nil
}
