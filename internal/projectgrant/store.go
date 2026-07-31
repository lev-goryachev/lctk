package projectgrant

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lev-goryachev/lctk/internal/lctkhome"
)

// FileName is the grant document inside the LCTK home directory.
const FileName = "grants.json"

type document struct {
	SchemaVersion int     `json:"schema_version"`
	Grants        []Grant `json:"grants"`
}

// ErrSchemaTooNew reports a document written by a newer LCTK.
var ErrSchemaTooNew = errors.New("grants were written by a newer version of LCTK")

// Path returns the grant file path without creating anything.
func Path() (string, error) {
	dir, err := lctkhome.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// Load reads the grants, returning an empty set when no file exists yet.
//
// A corrupt file is an error rather than a silent reset: discarding grants would
// look like a clean slate while leaving configured clients pointing at
// credentials that no longer exist.
func Load() (*Set, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return New(), nil
		}
		return nil, fmt.Errorf("read grants %q: %w", path, err)
	}

	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("grants %q are not valid JSON: %w", path, err)
	}

	version := doc.SchemaVersion
	if version == 0 {
		version = 1
	}
	if version > SchemaVersion {
		return nil, fmt.Errorf("grants %q: %w: found schema version %d, this build understands %d",
			path, ErrSchemaTooNew, version, SchemaVersion)
	}

	grants := doc.Grants
	if grants == nil {
		grants = []Grant{}
	}
	set := &Set{grants: grants}
	if err := set.validate(); err != nil {
		return nil, fmt.Errorf("grants %q: %w", path, err)
	}
	return set, nil
}

func (s *Set) validate() error {
	seenID := make(map[string]bool, len(s.grants))
	seenToken := make(map[string]bool, len(s.grants))
	for _, grant := range s.grants {
		switch {
		case grant.ID == "":
			return errors.New("a grant has an empty id")
		case grant.Token == "":
			return fmt.Errorf("grant %q has an empty token", grant.ID)
		case len(grant.ProjectIDs) == 0:
			return fmt.Errorf("grant %q permits no projects", grant.ID)
		}
		if seenID[grant.ID] {
			return fmt.Errorf("grant id %q appears twice", grant.ID)
		}
		// Two grants sharing a token would make the resolved scope ambiguous,
		// which is exactly the property project isolation depends on.
		if seenToken[grant.Token] {
			return fmt.Errorf("two grants share one token, so the permitted scope is ambiguous")
		}
		seenID[grant.ID] = true
		seenToken[grant.Token] = true
	}
	return nil
}

// Save writes the grants atomically with owner-only permissions.
func (s *Set) Save() error {
	if err := s.validate(); err != nil {
		return err
	}

	dir, err := lctkhome.EnsureDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, FileName)

	doc := document{SchemaVersion: SchemaVersion, Grants: s.List()}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode grants: %w", err)
	}
	encoded = append(encoded, '\n')

	temp, err := os.CreateTemp(dir, FileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary grant file in %q: %w", dir, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	// os.CreateTemp already creates the file with owner-only permissions, so the
	// token is never briefly readable by another user; the explicit Chmod below
	// only restates that after the write.
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary grant file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("flush temporary grant file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary grant file: %w", err)
	}
	if err := os.Chmod(tempName, 0o600); err != nil {
		return fmt.Errorf("restrict temporary grant file permissions: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace grant file %q: %w", path, err)
	}
	return nil
}
