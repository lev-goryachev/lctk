package projectregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lev-goryachev/lctk/internal/lctkhome"
)

// FileName is the registry document inside the LCTK home directory.
const FileName = "registry.json"

// document is the on-disk shape. The schema version is first so that a
// hand-inspected file shows it immediately.
type document struct {
	SchemaVersion int       `json:"schema_version"`
	Projects      []Project `json:"projects"`
}

// ErrSchemaTooNew reports a document written by a newer LCTK.
var ErrSchemaTooNew = errors.New("registry was written by a newer version of LCTK")

// Path returns the registry file path without creating anything.
func Path() (string, error) {
	dir, err := lctkhome.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// Load reads the registry, returning an empty one when no file exists yet.
//
// A missing file is the normal first-run state and is not an error. A corrupt
// file is an error rather than a silent reset, because discarding registrations
// would detach projects from their persistent data.
func Load() (*Registry, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return New(), nil
		}
		return nil, fmt.Errorf("read registry %q: %w", path, err)
	}

	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("registry %q is not valid JSON: %w", path, err)
	}

	registry, err := migrate(doc)
	if err != nil {
		return nil, fmt.Errorf("registry %q: %w", path, err)
	}
	if err := registry.validate(); err != nil {
		return nil, fmt.Errorf("registry %q: %w", path, err)
	}
	return registry, nil
}

// migrate brings a stored document up to the current schema version.
//
// Version 0 covers a document written before the field existed; it is read as
// version 1 because no released schema predates it. Each future step is added
// here as an explicit case so that an upgrade path is reviewable.
func migrate(doc document) (*Registry, error) {
	version := doc.SchemaVersion
	if version == 0 {
		version = 1
	}
	if version > SchemaVersion {
		return nil, fmt.Errorf("%w: found schema version %d, this build understands %d",
			ErrSchemaTooNew, version, SchemaVersion)
	}

	projects := doc.Projects
	if projects == nil {
		projects = []Project{}
	}

	// Defaults for records written before a field carried meaning.
	for i := range projects {
		if projects[i].Profile == "" {
			projects[i].Profile = ProfileMinimal
		}
	}

	return &Registry{schemaVersion: SchemaVersion, projects: projects}, nil
}

// validate rejects a document that would break scope guarantees, such as a
// duplicated identifier that would make a route ambiguous.
func (r *Registry) validate() error {
	seenID := make(map[string]string, len(r.projects))
	for _, p := range r.projects {
		switch {
		case p.ID == "":
			return errors.New("a registration has an empty project id")
		case p.Path == "":
			return fmt.Errorf("registration %q has an empty host path", p.ID)
		case p.Key == "":
			return fmt.Errorf("registration %q has an empty comparison key", p.ID)
		case !p.Profile.Valid():
			return fmt.Errorf("registration %q has %w: %q", p.ID, ErrInvalidProfile, p.Profile)
		}
		if other, clash := seenID[p.ID]; clash {
			return fmt.Errorf("project id %q is used by both %q and %q", p.ID, other, p.Path)
		}
		seenID[p.ID] = p.Path
	}
	return nil
}

// Save writes the registry atomically.
//
// The document is written to a temporary file in the same directory and renamed
// over the target, so an interrupted write cannot leave a half-written registry
// that would detach projects from their data.
func (r *Registry) Save() error {
	if err := r.validate(); err != nil {
		return err
	}

	dir, err := lctkhome.EnsureDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, FileName)

	doc := document{SchemaVersion: SchemaVersion, Projects: r.List()}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode registry: %w", err)
	}
	encoded = append(encoded, '\n')

	temp, err := os.CreateTemp(dir, FileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary registry in %q: %w", dir, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary registry: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("flush temporary registry: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary registry: %w", err)
	}
	if err := os.Chmod(tempName, 0o600); err != nil {
		return fmt.Errorf("restrict temporary registry permissions: %w", err)
	}

	// os.Rename replaces an existing file on both target platforms.
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace registry %q: %w", path, err)
	}
	return nil
}
