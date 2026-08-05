// Package codexsetup applies the complete explicit Codex client configuration
// selected from the Admin UI. Credential delivery remains process-scoped and is
// performed by the accepted codex launch path.
package codexsetup

import (
	"fmt"
	"time"

	"github.com/lev-goryachev/lctk/internal/codexconfig"
	"github.com/lev-goryachev/lctk/internal/localapi"
	"github.com/lev-goryachev/lctk/internal/projectgrant"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

// Result describes the exact durable user configuration changed by consent.
type Result struct {
	ConfigPath  string `json:"config_path"`
	Environment string `json:"environment"`
	Changed     bool   `json:"changed"`
}

// Configure merges only LCTK's marker-delimited entry and persists the existing
// scoped grant in the current user's environment.
func Configure(project projectregistry.Project, now time.Time) (Result, error) {
	path, err := codexconfig.Path()
	if err != nil {
		return Result{}, err
	}
	document, err := codexconfig.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	variable := projectgrant.EnvVarName(project.ID)
	entry := codexconfig.Entry{Name: codexconfig.EntryName(project.ID),
		URL:               fmt.Sprintf("http://%s/projects/%s/mcp", localapi.DefaultAddress, project.ID),
		BearerTokenEnvVar: variable, Enabled: true}
	updated, err := codexconfig.Merge(document, project.ID, entry, false)
	if err != nil {
		return Result{}, err
	}
	changed := updated != document
	if changed {
		if _, err := codexconfig.WriteFile(path, updated); err != nil {
			return Result{}, err
		}
	}
	grants, err := projectgrant.Load()
	if err != nil {
		return Result{}, err
	}
	if _, err := grants.ForProject(project.ID, now); err != nil {
		return Result{}, err
	}
	return Result{ConfigPath: path, Environment: variable, Changed: changed}, nil
}
