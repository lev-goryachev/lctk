// Package projectregistry stores the authoritative binding between a project_id
// and a canonical host path.
//
// This registry is the only authority on project scope. Per docs/security.md a
// project_id or path supplied by a model, a tool argument, or a repository
// manifest is never authoritative, and nothing in this package accepts a host
// path from any of those sources.
//
// Registration performs no container, network, or index work. Slice 1.1 must
// work without starting services.
package projectregistry

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/internal/projectpath"
)

// SchemaVersion is the current on-disk schema version of the registry document.
// It is incremented whenever a stored field changes meaning, and Load migrates
// older documents forward.
const SchemaVersion = 1

// Profile selects how much machinery a project asks for. It is declared here
// rather than taken from a manifest because it affects host resource use.
type Profile string

const (
	// ProfileMinimal is the default: exact search only, smallest footprint.
	ProfileMinimal Profile = "minimal"
	// ProfileFull requests the full capability set for the project.
	ProfileFull Profile = "full"
)

// Valid reports whether the profile is one LCTK understands.
func (p Profile) Valid() bool {
	return p == ProfileMinimal || p == ProfileFull
}

// Sentinel errors, so callers and the CLI can map failures to typed exit
// behavior rather than matching strings.
var (
	// ErrNotFound reports that no registration matches the given reference.
	ErrNotFound = errors.New("project not found")
	// ErrAlreadyRegistered reports that the folder is already registered.
	ErrAlreadyRegistered = errors.New("folder is already registered")
	// ErrAmbiguousReference reports that a name or prefix matched several projects.
	ErrAmbiguousReference = errors.New("project reference is ambiguous")
	// ErrInvalidProfile reports an unknown profile.
	ErrInvalidProfile = errors.New("unknown profile")
)

// Project is one registration record.
//
// Path is the authoritative host path. Key exists so that path aliases collapse
// to one project, and it is never used as a mount path.
type Project struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Path            string    `json:"path"`
	Key             string    `json:"key"`
	CaseInsensitive bool      `json:"case_insensitive"`
	Profile         Profile   `json:"profile"`
	RegisteredAt    time.Time `json:"registered_at"`
	// ManifestPresent records whether a tracked manifest was found at
	// registration time. The manifest's contents are read on demand rather than
	// cached, so a repository edit cannot silently persist into host state.
	ManifestPresent bool `json:"manifest_present"`
	// ResourceMode overrides the machine-wide background-load policy for this
	// project. Empty means the machine policy applies.
	//
	// It lives in the registry rather than in the repository manifest because how
	// much of a machine a project may use is the machine owner's decision. A
	// repository author has no say in it, and the manifest has no field that could
	// express one.
	ResourceMode string `json:"resource_mode,omitempty"`
}

// DuplicateError reports which existing registration already covers a folder.
type DuplicateError struct {
	Existing Project
	// SameFile records that the operating system confirmed both paths name one
	// folder, as opposed to the comparison keys merely matching.
	SameFile bool
}

func (e *DuplicateError) Error() string {
	how := "the same canonical path"
	if e.SameFile {
		how = "the same folder on disk"
	}
	return fmt.Sprintf("%v: %s is already registered as %s (%s)",
		ErrAlreadyRegistered, e.Existing.Path, e.Existing.ID, how)
}

func (e *DuplicateError) Unwrap() error { return ErrAlreadyRegistered }

// Registry is an in-memory view of the registration set. Callers obtain one from
// Load, mutate it, and persist it with Save.
type Registry struct {
	schemaVersion int
	projects      []Project
}

// New returns an empty registry at the current schema version.
func New() *Registry {
	return &Registry{schemaVersion: SchemaVersion}
}

// List returns the registrations ordered by identifier, so command output and
// generated configuration are reproducible.
func (r *Registry) List() []Project {
	out := make([]Project, len(r.projects))
	copy(out, r.projects)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len reports the number of registrations.
func (r *Registry) Len() int { return len(r.projects) }

// Add registers a canonical path.
//
// The caller supplies an already-canonicalized path so that this package cannot
// be handed a raw, unresolved value. A folder that is already registered under
// any alias is rejected rather than silently deduplicated, because the user
// asked for a new project and should learn that it exists.
func (r *Registry) Add(c projectpath.Canonical, profile Profile, manifestPresent bool) (Project, error) {
	if profile == "" {
		profile = ProfileMinimal
	}
	if !profile.Valid() {
		return Project{}, fmt.Errorf("%w: %q", ErrInvalidProfile, profile)
	}

	if existing, sameFile, found := r.matching(c); found {
		return Project{}, &DuplicateError{Existing: existing, SameFile: sameFile}
	}

	project := Project{
		ID:              projectpath.DeriveID(c),
		Name:            c.Base,
		Path:            c.Display,
		Key:             c.Key,
		CaseInsensitive: c.CaseInsensitive,
		Profile:         profile,
		RegisteredAt:    time.Now().UTC().Truncate(time.Second),
		ManifestPresent: manifestPresent,
	}

	// An identifier collision without a duplicate path would mean two different
	// folders share a digest. Refuse rather than overwrite.
	for _, p := range r.projects {
		if p.ID == project.ID {
			return Project{}, fmt.Errorf("project id %q already belongs to %s", p.ID, p.Path)
		}
	}

	r.projects = append(r.projects, project)
	return project, nil
}

// matching reports an existing registration that names the same folder as c, and
// whether the operating system confirmed it.
//
// The operating system is the authority: os.SameFile catches aliases that string
// comparison cannot, such as a junction, a substituted drive, or a UNC spelling
// of a local path. When both paths are present and are different folders, a
// matching comparison key is a false positive from case folding and is ignored.
// The key is the fallback only for a registered folder that is currently
// unavailable, for example on a disconnected volume.
func (r *Registry) matching(c projectpath.Canonical) (project Project, sameFile bool, found bool) {
	candidate, candidateErr := os.Stat(c.Display)

	for _, existing := range r.projects {
		if candidateErr == nil {
			if info, err := os.Stat(existing.Path); err == nil {
				if os.SameFile(candidate, info) {
					return existing, true, true
				}
				continue
			}
		}
		if existing.Key == c.Key {
			return existing, false, true
		}
	}
	return Project{}, false, false
}

// Remove deletes a registration. Persistent project data is not touched, per the
// remove versus purge distinction in docs/project-lifecycle.md.
// SetResourceMode records this project's background-load override. An empty mode
// clears it, so the project follows the machine policy again.
//
// The value is not validated here. The registry stores host decisions; deciding
// which modes exist belongs to the package that defines them, and duplicating
// that list here would give two answers to one question.
func (r *Registry) SetResourceMode(projectID, mode string) error {
	for i := range r.projects {
		if r.projects[i].ID == projectID {
			r.projects[i].ResourceMode = mode
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrNotFound, projectID)
}

func (r *Registry) Remove(reference string) (Project, error) {
	project, err := r.Resolve(reference)
	if err != nil {
		return Project{}, err
	}
	for i, p := range r.projects {
		if p.ID == project.ID {
			r.projects = append(r.projects[:i], r.projects[i+1:]...)
			return project, nil
		}
	}
	return Project{}, ErrNotFound
}

// Resolve finds one registration from a user-supplied reference.
//
// A reference may be an exact identifier, an exact name, or a path naming a
// registered folder. Convenience matching by name or identifier prefix is
// allowed only when it is unambiguous; this is a local CLI affordance and is
// never used to establish scope for an MCP request, where the route supplies the
// identifier exactly.
func (r *Registry) Resolve(reference string) (Project, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return Project{}, fmt.Errorf("%w: no project reference was given", ErrNotFound)
	}

	for _, p := range r.projects {
		if p.ID == reference {
			return p, nil
		}
	}

	// A path is accepted only after canonicalization, so an alias of a
	// registered folder still resolves.
	if canonical, err := projectpath.Resolve(reference); err == nil {
		if existing, _, found := r.matching(canonical); found {
			return existing, nil
		}
	}

	var matches []Project
	for _, p := range r.projects {
		if strings.EqualFold(p.Name, reference) || strings.HasPrefix(p.ID, reference) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Project{}, fmt.Errorf("%w: %q", ErrNotFound, reference)
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		sort.Strings(ids)
		return Project{}, fmt.Errorf("%w: %q matches %s", ErrAmbiguousReference, reference, strings.Join(ids, ", "))
	}
}
