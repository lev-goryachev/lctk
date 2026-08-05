package inference

import "github.com/lev-goryachev/lctk/internal/containerruntime"

// NewRuntimeManager constructs the pinned production lifecycle over LCTK's
// private Podman connection. The provider never comes from PATH or user state.
func NewRuntimeManager() (*Manager, error) { return NewManager(containerruntime.Runner{}) }
