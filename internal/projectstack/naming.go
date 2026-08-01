// Package projectstack generates and drives the per-project container stack.
//
// Each registered project gets its own Compose project, network, and volume, per
// [ADR-0003]. The source is mounted read-only into the code-intel boundary, so a
// project cannot write to its own working tree through this path.
//
// Generated configuration is derived state: it is rewritten from the registry
// whenever a project starts, and it is never a source of truth for project
// identity or for the host path.
//
// [ADR-0003]: ../../docs/adr/0003-reusable-images-and-project-stacks.md
package projectstack

import (
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/lev-goryachev/lctk/internal/lctkhome"
)

const (
	// ServiceName is the single service in a Slice 1.2 stack.
	ServiceName = "code-intel"
	// WorkspaceMount is where the project source appears inside the container.
	WorkspaceMount = "/workspace"
	// StateMount is where per-project persistent state lives inside the container.
	StateMount = "/var/lib/lctk"
	// ServicePort is the port the code-intel service listens on inside the
	// container. It is fixed because each project has its own network namespace;
	// the host side is published on an ephemeral loopback port instead, so two
	// projects can never contend for one number.
	ServicePort = 8080
	// ImageRepository is the reusable image every project shares.
	ImageRepository = "lctk/code-intel"
	// resourcePrefix keeps every Docker resource LCTK creates identifiable and
	// distinguishable from unrelated Compose projects on the same machine.
	resourcePrefix = "lctk"
)

// composeNamePattern is what Compose accepts for a project name. Project
// identifiers are generated to satisfy it, and Names verifies rather than
// assuming.
var composeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Names holds every Docker resource name derived from a project identifier.
//
// The names are a pure function of the identifier, so they are stable across
// restarts and reinstalls and can be recomputed rather than stored.
type Names struct {
	ProjectID     string
	ComposeName   string
	Network       string
	Volume        string
	ContainerName string
	Image         string
}

// DeriveNames computes the resource names for a project identifier.
//
// ADR-0003 left Compose resource naming to be specified; this is that
// specification. The image tag follows the unified product version from
// [ADR-0007], so an upgraded LCTK asks for a matching image rather than silently
// reusing an older one.
//
// [ADR-0007]: ../../docs/adr/0007-unified-versioning.md
func DeriveNames(projectID string) (Names, error) {
	if projectID == "" {
		return Names{}, fmt.Errorf("%w: project id is empty", ErrInvalidProject)
	}
	if !composeNamePattern.MatchString(projectID) {
		return Names{}, fmt.Errorf("%w: project id %q is not usable as a Compose project name",
			ErrInvalidProject, projectID)
	}

	base := resourcePrefix + "-" + projectID
	return Names{
		ProjectID:     projectID,
		ComposeName:   base,
		Network:       base + "-net",
		Volume:        base + "-state",
		ContainerName: base + "-" + ServiceName,
		Image:         ImageRepository + ":" + buildinfo.Version,
	}, nil
}

// StackDir is the per-project directory holding generated configuration.
//
// It lives under the LCTK home rather than in the repository, because generated
// configuration must not be committable and must not be editable by whoever
// controls the project's source.
func StackDir(projectID string) (string, error) {
	home, err := lctkhome.Dir()
	if err != nil {
		return "", err
	}
	if projectID == "" || !composeNamePattern.MatchString(projectID) {
		return "", fmt.Errorf("%w: project id %q cannot be used as a directory name",
			ErrInvalidProject, projectID)
	}
	return filepath.Join(home, "projects", projectID), nil
}

// ComposeFilePath is the generated Compose file for a project.
func ComposeFilePath(projectID string) (string, error) {
	dir, err := StackDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "compose.yaml"), nil
}
