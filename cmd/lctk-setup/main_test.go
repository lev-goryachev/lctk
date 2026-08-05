package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lev-goryachev/lctk/internal/lctkhome"
)

func TestInspectSelectionRejectsUnsafeLocationsBeforeBuildingAPlan(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	_, _, err := inspectSelection(context.Background(), setupRequest{}, lctkhome.Locations{
		InstallDir: root, RuntimeDataDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("inspectSelection accepted a drive root as the installation directory")
	}
}
