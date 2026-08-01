package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/internal/codexconfig"
	"github.com/lev-goryachev/lctk/internal/daemon"
	"github.com/lev-goryachev/lctk/internal/projectgrant"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

const codexUsage = `Usage:
  lctk codex status [--json] [PROJECT]
  lctk codex config [--apply] [--force] [--remove] [--listen ADDRESS] [--json] PROJECT
  lctk codex env [--json] [--reveal] PROJECT
  lctk codex launch [--editor NAME] [--force] [--dry-run] PROJECT [-- EDITOR_ARGS]

Codex reads MCP servers from CODEX_HOME/config.toml and refuses an inline
credential, so LCTK writes a configuration entry that names an environment
variable and delivers the token through a process it starts. See ADR-0014.

config prints the entry and writes only with --apply. LCTK changes only the
region it owns in a file it shares with other tools.
`

// defaultEditor is the command lctk codex launch runs. VS Code installs it on
// PATH as part of its own setup, so LCTK does not go looking for an install.
const defaultEditor = "code"

func runCodex(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, codexUsage)
		return errors.New("a codex subcommand is required")
	}
	switch args[0] {
	case "status":
		return runCodexStatus(args[1:], stdout)
	case "config":
		return runCodexConfig(args[1:], stdout)
	case "env":
		return runCodexEnv(args[1:], stdout)
	case "launch":
		return runCodexLaunch(args[1:], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, codexUsage)
		return nil
	default:
		fmt.Fprint(stderr, codexUsage)
		return fmt.Errorf("unknown codex subcommand %q", args[0])
	}
}

// codexProjectView is what a caller needs to tell a working integration from a
// broken one without being shown the token.
type codexProjectView struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	EntryName   string `json:"entry_name"`
	Endpoint    string `json:"endpoint"`
	TokenEnvVar string `json:"token_env_var"`
	// Placement reports how the entry appears in the Codex configuration.
	Placement string `json:"placement"`
	// TokenEnvVarPresent reports whether the variable is set in the environment
	// LCTK itself was started with. It says nothing about the environment an
	// already-running editor inherited, which is the case ADR-0014 cannot detect
	// from here.
	TokenEnvVarPresent bool `json:"token_env_var_present"`
	GrantIssued        bool `json:"grant_issued"`
}

type codexStatusView struct {
	CodexHome    string             `json:"codex_home"`
	ConfigPath   string             `json:"config_path"`
	ConfigExists bool               `json:"config_exists"`
	Projects     []codexProjectView `json:"projects"`
}

func runCodexStatus(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("codex status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the status as JSON")
	listen := flags.String("listen", daemon.DefaultAddress, "daemon listen address the endpoint is built from")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: lctk codex status [--json] [PROJECT]")
	}

	path, err := codexconfig.Path()
	if err != nil {
		return err
	}
	home, err := codexconfig.Home()
	if err != nil {
		return err
	}
	document, err := codexconfig.ReadFile(path)
	if err != nil {
		return err
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}
	projects := registry.List()
	if flags.NArg() == 1 {
		project, err := registry.Resolve(flags.Arg(0))
		if err != nil {
			return err
		}
		projects = []projectregistry.Project{project}
	}

	grants, err := projectgrant.Load()
	if err != nil {
		return err
	}
	now := time.Now()

	view := codexStatusView{
		CodexHome:    home,
		ConfigPath:   path,
		ConfigExists: document != "",
		Projects:     make([]codexProjectView, 0, len(projects)),
	}
	for _, project := range projects {
		_, grantErr := grants.ForProject(project.ID, now)
		variable := projectgrant.EnvVarName(project.ID)
		_, present := os.LookupEnv(variable)
		view.Projects = append(view.Projects, codexProjectView{
			ProjectID:          project.ID,
			Name:               project.Name,
			EntryName:          codexconfig.EntryName(project.ID),
			Endpoint:           projectEndpoint(*listen, project.ID),
			TokenEnvVar:        variable,
			Placement:          string(codexconfig.Locate(document, project.ID).Placement),
			TokenEnvVarPresent: present,
			GrantIssued:        grantErr == nil,
		})
	}

	if *asJSON {
		return writeJSON(stdout, view)
	}
	fmt.Fprintf(stdout, "Codex home:   %s\n", view.CodexHome)
	fmt.Fprintf(stdout, "Config file:  %s", view.ConfigPath)
	if !view.ConfigExists {
		fmt.Fprint(stdout, "  (does not exist yet)")
	}
	fmt.Fprint(stdout, "\n")
	if len(view.Projects) == 0 {
		fmt.Fprint(stdout, "\nNo projects are registered.\n")
		return nil
	}
	for _, project := range view.Projects {
		fmt.Fprintf(stdout, "\n%s  (%s)\n", project.ProjectID, project.Name)
		fmt.Fprintf(stdout, "  entry:     %s  [%s]\n", project.EntryName, project.Placement)
		fmt.Fprintf(stdout, "  endpoint:  %s\n", project.Endpoint)
		fmt.Fprintf(stdout, "  env var:   %s  (%s in this shell)\n", project.TokenEnvVar, presence(project.TokenEnvVarPresent))
		if !project.GrantIssued {
			fmt.Fprint(stdout, "  grant:     none; run lctk grant show to issue one\n")
		}
	}
	fmt.Fprint(stdout, "\nA running editor keeps the environment it started with, so a variable set\n")
	fmt.Fprint(stdout, "after the editor started is not visible to it.\n")
	return nil
}

func presence(present bool) string {
	if present {
		return "set"
	}
	return "not set"
}

type codexConfigView struct {
	ProjectID   string `json:"project_id"`
	EntryName   string `json:"entry_name"`
	Endpoint    string `json:"endpoint"`
	TokenEnvVar string `json:"token_env_var"`
	ConfigPath  string `json:"config_path"`
	Placement   string `json:"placement"`
	// Region is the configuration text LCTK owns for this project. It is empty
	// when the entry is being removed.
	Region string `json:"region,omitempty"`
	// Changed reports whether applying would alter the file at all.
	Changed bool   `json:"changed"`
	Applied bool   `json:"applied"`
	Backup  string `json:"backup,omitempty"`
}

func runCodexConfig(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("codex config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the result as JSON")
	apply := flags.Bool("apply", false, "write the entry into the Codex configuration")
	force := flags.Bool("force", false, "replace a same-named entry LCTK did not generate")
	remove := flags.Bool("remove", false, "remove the LCTK entry for this project")
	listen := flags.String("listen", daemon.DefaultAddress, "daemon listen address the endpoint is built from")
	startupTimeout := flags.Float64("startup-timeout", 0, "startup_timeout_sec to emit, omitted when zero")
	toolTimeout := flags.Float64("tool-timeout", 0, "tool_timeout_sec to emit, omitted when zero")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk codex config [--apply] [--force] [--remove] PROJECT")
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}
	project, err := registry.Resolve(flags.Arg(0))
	if err != nil {
		return err
	}

	path, err := codexconfig.Path()
	if err != nil {
		return err
	}
	document, err := codexconfig.ReadFile(path)
	if err != nil {
		return err
	}

	view := codexConfigView{
		ProjectID:   project.ID,
		EntryName:   codexconfig.EntryName(project.ID),
		Endpoint:    projectEndpoint(*listen, project.ID),
		TokenEnvVar: projectgrant.EnvVarName(project.ID),
		ConfigPath:  path,
		Placement:   string(codexconfig.Locate(document, project.ID).Placement),
	}

	var updated string
	if *remove {
		var removed bool
		updated, removed, err = codexconfig.Remove(document, project.ID)
		if err != nil {
			return err
		}
		view.Changed = removed
	} else {
		entry := codexconfig.Entry{
			Name:              view.EntryName,
			URL:               view.Endpoint,
			BearerTokenEnvVar: view.TokenEnvVar,
			StartupTimeoutSec: *startupTimeout,
			ToolTimeoutSec:    *toolTimeout,
			Enabled:           true,
		}
		updated, err = codexconfig.Merge(document, project.ID, entry, *force)
		if err != nil {
			return explainMergeFailure(err, view.EntryName, path)
		}
		region := codexconfig.Locate(updated, project.ID)
		view.Region = regionText(updated, region)
		view.Changed = updated != document
	}

	if *apply && view.Changed {
		backup, err := codexconfig.WriteFile(path, updated)
		if err != nil {
			return err
		}
		view.Applied = true
		view.Backup = backup
	}

	if *asJSON {
		return writeJSON(stdout, view)
	}
	return printCodexConfig(stdout, view, *apply, *remove)
}

func printCodexConfig(stdout io.Writer, view codexConfigView, apply, remove bool) error {
	fmt.Fprintf(stdout, "Config file: %s\n", view.ConfigPath)
	switch {
	case remove && !view.Changed:
		fmt.Fprintf(stdout, "No LCTK entry for %s is present; nothing to remove.\n", view.ProjectID)
		return nil
	case remove && view.Applied:
		fmt.Fprintf(stdout, "Removed the entry for %s. Backup: %s\n", view.ProjectID, view.Backup)
		return nil
	case remove:
		fmt.Fprintf(stdout, "Would remove the entry for %s. Re-run with --apply.\n", view.ProjectID)
		return nil
	}

	fmt.Fprint(stdout, "\n")
	fmt.Fprint(stdout, view.Region)
	fmt.Fprint(stdout, "\n")
	switch {
	case view.Applied:
		fmt.Fprintf(stdout, "Written. Backup: %s\n", view.Backup)
	case !view.Changed:
		fmt.Fprint(stdout, "Already present and unchanged.\n")
	case apply:
		fmt.Fprint(stdout, "Nothing to write.\n")
	default:
		fmt.Fprint(stdout, "Not written. Re-run with --apply to write it.\n")
	}
	fmt.Fprintf(stdout, "\nThe token is not in this file. Deliver it with:\n  lctk codex launch %s\n", view.ProjectID)
	return nil
}

// explainMergeFailure turns a refusal into an instruction, because the caller
// most likely to hit one is an agent that has to decide what to do next.
func explainMergeFailure(err error, entryName, path string) error {
	switch {
	case errors.Is(err, codexconfig.ErrForeignEntry):
		return fmt.Errorf("%q in %s was not generated by LCTK; inspect it, then re-run with --force to replace it", entryName, path)
	case errors.Is(err, codexconfig.ErrForeignInlineEntry):
		return fmt.Errorf("%q in %s is written as an inline key; remove it by hand, then re-run", entryName, path)
	case errors.Is(err, codexconfig.ErrExistingInvalid):
		return fmt.Errorf("%s cannot be parsed, so Codex is not loading it either: %w", path, err)
	default:
		return err
	}
}

func regionText(document string, location codexconfig.Location) string {
	if location.Placement == codexconfig.PlacementAbsent {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n")
	if location.Last >= len(lines) {
		return ""
	}
	return strings.Join(lines[location.First:location.Last+1], "\n") + "\n"
}

type codexEnvView struct {
	ProjectID   string `json:"project_id"`
	TokenEnvVar string `json:"token_env_var"`
	Token       string `json:"token,omitempty"`
	// PersistCommand is printed, never run. ADR-0014 keeps a durable change to
	// the machine an act the operator performs.
	PersistCommand string `json:"persist_command"`
	Present        bool   `json:"present_in_this_environment"`
}

func runCodexEnv(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("codex env", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the result as JSON")
	reveal := flags.Bool("reveal", false, "include the token value")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk codex env [--json] [--reveal] PROJECT")
	}

	project, grant, err := resolveProjectGrant(flags.Arg(0))
	if err != nil {
		return err
	}
	variable := projectgrant.EnvVarName(project.ID)
	_, present := os.LookupEnv(variable)

	view := codexEnvView{
		ProjectID:      project.ID,
		TokenEnvVar:    variable,
		PersistCommand: persistCommand(variable, grant.Token, *reveal),
		Present:        present,
	}
	if *reveal {
		view.Token = grant.Token
	}

	if *asJSON {
		return writeJSON(stdout, view)
	}
	fmt.Fprintf(stdout, "env var: %s  (%s in this shell)\n", view.TokenEnvVar, presence(view.Present))
	if *reveal {
		fmt.Fprintf(stdout, "token:   %s\n", view.Token)
	} else {
		fmt.Fprint(stdout, "token:   hidden, pass --reveal to print it\n")
	}
	fmt.Fprintf(stdout, "\nlctk codex launch %s delivers this without setting anything durable.\n", view.ProjectID)
	fmt.Fprint(stdout, "To set it yourself instead, run:\n")
	fmt.Fprintf(stdout, "  %s\n", view.PersistCommand)
	fmt.Fprint(stdout, "then start the editor again, because a running process keeps its own environment.\n")
	return nil
}

// persistCommand renders the command the operator would run to set the variable
// durably. LCTK never runs it.
func persistCommand(variable, token string, reveal bool) string {
	value := "<token>"
	if reveal {
		value = token
	}
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("setx %s %q", variable, value)
	}
	return fmt.Sprintf("export %s=%q", variable, value)
}

type codexLaunchView struct {
	ProjectID   string   `json:"project_id"`
	Editor      string   `json:"editor"`
	EditorPath  string   `json:"editor_path"`
	Arguments   []string `json:"arguments"`
	TokenEnvVar string   `json:"token_env_var"`
	Started     bool     `json:"started"`
	PID         int      `json:"pid,omitempty"`
	// AlreadyRunning is reported when LCTK could tell that an instance of the
	// editor was already running. A new window from a running instance inherits
	// that process's environment, not the one LCTK would supply.
	AlreadyRunning string `json:"already_running"`
}

func runCodexLaunch(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("codex launch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the result as JSON")
	editor := flags.String("editor", defaultEditor, "editor command to start")
	force := flags.Bool("force", false, "start even when the editor already appears to be running")
	dryRun := flags.Bool("dry-run", false, "report what would be started without starting it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 1 {
		return errors.New("usage: lctk codex launch [--editor NAME] PROJECT [-- EDITOR_ARGS]")
	}

	project, grant, err := resolveProjectGrant(flags.Arg(0))
	if err != nil {
		return err
	}

	// The editor opens the project folder unless the caller supplied their own
	// arguments, which is the whole point of starting it from here.
	arguments := flags.Args()[1:]
	if len(arguments) == 0 {
		arguments = []string{project.Path}
	}

	path, err := exec.LookPath(*editor)
	if err != nil {
		return fmt.Errorf("cannot find the editor command %q on PATH: %w", *editor, err)
	}

	variable := projectgrant.EnvVarName(project.ID)
	view := codexLaunchView{
		ProjectID:      project.ID,
		Editor:         *editor,
		EditorPath:     path,
		Arguments:      arguments,
		TokenEnvVar:    variable,
		AlreadyRunning: string(editorRunning(*editor)),
	}

	if view.AlreadyRunning == string(editorRunningYes) && !*force && !*dryRun {
		return fmt.Errorf("%s already appears to be running, and a new window from a running instance "+
			"inherits that process's environment rather than the token; close it and re-run, or pass --force", *editor)
	}

	if !*dryRun {
		command := exec.Command(path, arguments...)
		command.Env = append(os.Environ(), variable+"="+grant.Token)
		command.Dir = project.Path
		if err := command.Start(); err != nil {
			return fmt.Errorf("start %s: %w", *editor, err)
		}
		view.Started = true
		if command.Process != nil {
			view.PID = command.Process.Pid
		}
		// The editor outlives this command. Releasing avoids leaving a zombie
		// entry behind on the platforms that keep one.
		_ = command.Process.Release()
	}

	if *asJSON {
		return writeJSON(stdout, view)
	}
	if *dryRun {
		fmt.Fprintf(stdout, "Would start %s %s\n", view.EditorPath, strings.Join(view.Arguments, " "))
		fmt.Fprintf(stdout, "with %s set in its environment.\n", view.TokenEnvVar)
		return nil
	}
	fmt.Fprintf(stdout, "Started %s for %s with %s in its environment.\n", view.Editor, view.ProjectID, view.TokenEnvVar)
	if view.AlreadyRunning == string(editorRunningUnknown) {
		fmt.Fprintf(stdout, "LCTK cannot tell on this platform whether %s was already running. "+
			"If it was, close it and run this again.\n", view.Editor)
	}
	return nil
}

// resolveProjectGrant returns a project and a usable grant, issuing one if the
// project has none, so a project registered before grants existed still works.
func resolveProjectGrant(reference string) (projectregistry.Project, projectgrant.Grant, error) {
	registry, err := projectregistry.Load()
	if err != nil {
		return projectregistry.Project{}, projectgrant.Grant{}, err
	}
	project, err := registry.Resolve(reference)
	if err != nil {
		return projectregistry.Project{}, projectgrant.Grant{}, err
	}

	grants, err := projectgrant.Load()
	if err != nil {
		return projectregistry.Project{}, projectgrant.Grant{}, err
	}
	now := time.Now()
	grant, err := grants.ForProject(project.ID, now)
	if err != nil {
		grant, err = grants.EnsureForProject(project.ID, projectgrant.DefaultClient, now)
		if err != nil {
			return projectregistry.Project{}, projectgrant.Grant{}, err
		}
		if err := grants.Save(); err != nil {
			return projectregistry.Project{}, projectgrant.Grant{}, err
		}
	}
	return project, grant, nil
}

func projectEndpoint(listen, projectID string) string {
	return fmt.Sprintf("http://%s/projects/%s/mcp", listen, projectID)
}
