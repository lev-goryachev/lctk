package uninstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lev-goryachev/lctk/internal/containerruntime"
)

// cleanupManagedRuntimeResidue removes only paths whose names are bound to
// LCTK's one managed machine. Podman's shared parents are removed only when
// empty, so an unrelated Podman installation remains outside our authority.
func cleanupManagedRuntimeResidue(runtimeData, userHome string) error {
	if runtimeData == "" || userHome == "" {
		return errors.New("runtime data or user home directory is empty")
	}
	machineData := filepath.Join(runtimeData, "containers", "podman", "machine", "wsl")
	exact := []string{
		filepath.Join(machineData, containerruntime.MachineName),
		filepath.Join(machineData, "wsldist", containerruntime.MachineName),
	}
	cacheMatches, err := filepath.Glob(filepath.Join(machineData, containerruntime.MachineName+"-*"))
	if err != nil {
		return fmt.Errorf("resolve managed Podman machine cache: %w", err)
	}
	exact = append(exact, cacheMatches...)
	config := filepath.Join(userHome, ".config", "containers", "podman", "machine", "wsl")
	for _, suffix := range []string{".json", ".ign", ".lock"} {
		exact = append(exact, filepath.Join(config, containerruntime.MachineName+suffix))
	}
	for _, path := range exact {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove managed runtime residue %q: %w", path, err)
		}
	}
	// These directories are all Podman-owned containers. Removing an empty one
	// is cleanup; a non-empty one belongs to another machine and is preserved.
	for _, path := range []string{
		filepath.Join(machineData, "wsldist"), machineData,
		filepath.Join(runtimeData, "containers", "podman", "machine"),
		filepath.Join(runtimeData, "containers", "podman"),
		config,
		filepath.Dir(config), filepath.Dir(filepath.Dir(config)),
	} {
		if err := removeDirectoryIfEmpty(path); err != nil {
			return err
		}
	}
	return nil
}

func removeDirectoryIfEmpty(path string) error {
	entries, err := os.ReadDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect cleanup directory %q: %w", path, err)
	}
	if len(entries) != 0 {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove empty cleanup directory %q: %w", path, err)
	}
	return nil
}
