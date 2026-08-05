//go:build windows

package lctkhome

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const registryPath = `Software\LCTK`

func loadSavedLocations() (Locations, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, registryPath, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return Locations{}, nil
	}
	if err != nil {
		return Locations{}, fmt.Errorf("open LCTK location registry: %w", err)
	}
	defer key.Close()
	installDir, _, installErr := key.GetStringValue("InstallDir")
	runtimeDataDir, _, runtimeErr := key.GetStringValue("RuntimeDataDir")
	if installErr != nil && !errors.Is(installErr, registry.ErrNotExist) {
		return Locations{}, fmt.Errorf("read LCTK installation directory: %w", installErr)
	}
	if runtimeErr != nil && !errors.Is(runtimeErr, registry.ErrNotExist) {
		return Locations{}, fmt.Errorf("read LCTK runtime data directory: %w", runtimeErr)
	}
	return Locations{InstallDir: installDir, RuntimeDataDir: runtimeDataDir}, nil
}

func savePlatformLocations(locations Locations) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, registryPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetStringValue("InstallDir", locations.InstallDir); err != nil {
		return err
	}
	return key.SetStringValue("RuntimeDataDir", locations.RuntimeDataDir)
}

func clearPlatformLocations() error {
	err := registry.DeleteKey(registry.CURRENT_USER, registryPath)
	if errors.Is(err, registry.ErrNotExist) {
		return os.ErrNotExist
	}
	return err
}

func defaultRuntimeDataDir() (string, error) {
	profile := os.Getenv("USERPROFILE")
	if profile == "" {
		var err error
		profile, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve Podman data home: %w", err)
		}
	}
	// This is Podman 5.8's own Windows default. Saving it explicitly keeps an
	// existing partial installation attached to the same WSL distribution.
	return filepath.Join(profile, ".local", "share"), nil
}
