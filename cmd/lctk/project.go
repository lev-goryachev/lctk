package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/lev-goryachev/lctk/internal/projectmanifest"
	"github.com/lev-goryachev/lctk/internal/projectpath"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

const projectUsage = `Usage:
  lctk project add [--profile minimal|full] [--json] PATH
  lctk project status [--json] [PROJECT]
  lctk project remove [--json] PROJECT

PROJECT accepts a project id, a project name, an unambiguous id prefix, or the
path of a registered folder.
`

// projectView is the machine-readable shape of one registration.
//
// It is derived from the stored record rather than being the stored record, so
// that on-disk schema changes and command output can evolve separately.
type projectView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Path            string   `json:"path"`
	Profile         string   `json:"profile"`
	RegisteredAt    string   `json:"registered_at"`
	PathAvailable   bool     `json:"path_available"`
	ManifestPresent bool     `json:"manifest_present"`
	ManifestLocal   bool     `json:"manifest_local_override,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

func viewOf(p projectregistry.Project) projectView {
	_, err := os.Stat(p.Path)
	return projectView{
		ID:              p.ID,
		Name:            p.Name,
		Path:            p.Path,
		Profile:         string(p.Profile),
		RegisteredAt:    p.RegisteredAt.Format("2006-01-02T15:04:05Z"),
		PathAvailable:   err == nil,
		ManifestPresent: p.ManifestPresent,
	}
}

func runProject(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, projectUsage)
		return errors.New("a project subcommand is required")
	}
	switch args[0] {
	case "add":
		return runProjectAdd(args[1:], stdout, stderr)
	case "status":
		return runProjectStatus(args[1:], stdout)
	case "remove":
		return runProjectRemove(args[1:], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, projectUsage)
		return nil
	default:
		fmt.Fprint(stderr, projectUsage)
		return fmt.Errorf("unknown project subcommand %q", args[0])
	}
}

// runProjectAdd registers a folder. It starts no containers, networks, volumes,
// or indexing: Slice 1.1 is registration only.
func runProjectAdd(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("project add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("profile", "", "capability profile: minimal or full")
	asJSON := flags.Bool("json", false, "write the registration as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk project add [--profile minimal|full] [--json] PATH")
	}

	canonical, err := projectpath.Resolve(flags.Arg(0))
	if err != nil {
		return err
	}

	// The manifest is read for its safe declarations only. It cannot influence
	// the path that was just resolved.
	manifest, err := projectmanifest.Load(canonical.Display)
	if err != nil {
		return err
	}

	// Precedence: an explicit flag, then the manifest's proposal, then the
	// default. The manifest may propose a profile because that is a safe
	// declaration, but the operator's flag always wins.
	selected := projectregistry.Profile(*profile)
	if selected == "" {
		selected = projectregistry.Profile(manifest.Manifest.Profile)
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}
	project, err := registry.Add(canonical, selected, manifest.TrackedPresent)
	if err != nil {
		return err
	}
	if err := registry.Save(); err != nil {
		return err
	}

	view := viewOf(project)
	view.ManifestLocal = manifest.LocalPresent
	view.Warnings = manifest.Warnings

	if *asJSON {
		return writeJSON(stdout, view)
	}

	fmt.Fprintf(stdout, "Registered %s\n", view.ID)
	fmt.Fprintf(stdout, "  path:     %s\n", view.Path)
	fmt.Fprintf(stdout, "  profile:  %s\n", view.Profile)
	fmt.Fprintf(stdout, "  manifest: %s\n", manifestSummary(manifest))
	fmt.Fprint(stdout, "No services were started. Use lctk project status to inspect it.\n")
	for _, warning := range view.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	return nil
}

func manifestSummary(result projectmanifest.Result) string {
	switch {
	case result.TrackedPresent && result.LocalPresent:
		return projectmanifest.FileName + " with a local override"
	case result.TrackedPresent:
		return projectmanifest.FileName
	case result.LocalPresent:
		return projectmanifest.LocalFileName + " only"
	default:
		return "none"
	}
}

// runProjectStatus reports one project, or every project when no reference is
// given.
func runProjectStatus(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("project status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write status as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: lctk project status [--json] [PROJECT]")
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}

	if flags.NArg() == 1 {
		project, err := registry.Resolve(flags.Arg(0))
		if err != nil {
			return err
		}
		view := viewOf(project)
		// Re-read the manifest so that status reflects the repository as it is
		// now, not as it was at registration time.
		if manifest, err := projectmanifest.Load(project.Path); err == nil {
			view.ManifestPresent = manifest.TrackedPresent
			view.ManifestLocal = manifest.LocalPresent
			view.Warnings = manifest.Warnings
		} else {
			view.Warnings = append(view.Warnings, "manifest could not be read: "+err.Error())
		}
		if *asJSON {
			return writeJSON(stdout, view)
		}
		return writeProjectDetail(stdout, view)
	}

	projects := registry.List()
	views := make([]projectView, 0, len(projects))
	for _, p := range projects {
		views = append(views, viewOf(p))
	}

	if *asJSON {
		return writeJSON(stdout, views)
	}
	if len(views) == 0 {
		fmt.Fprint(stdout, "No projects are registered. Use lctk project add PATH.\n")
		return nil
	}
	return writeProjectTable(stdout, views)
}

func runProjectRemove(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("project remove", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the removed registration as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk project remove [--json] PROJECT")
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}
	project, err := registry.Remove(flags.Arg(0))
	if err != nil {
		return err
	}
	if err := registry.Save(); err != nil {
		return err
	}

	if *asJSON {
		return writeJSON(stdout, viewOf(project))
	}
	fmt.Fprintf(stdout, "Removed %s (%s)\n", project.ID, project.Path)
	fmt.Fprint(stdout, "Project data on disk was not deleted.\n")
	return nil
}

func writeProjectTable(output io.Writer, views []projectView) error {
	writer := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tPROFILE\tPATH\tPATH STATE")
	for _, v := range views {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", v.ID, v.Name, v.Profile, v.Path, pathState(v))
	}
	return writer.Flush()
}

func writeProjectDetail(output io.Writer, v projectView) error {
	fmt.Fprintf(output, "%s\n", v.ID)
	fmt.Fprintf(output, "  name:       %s\n", v.Name)
	fmt.Fprintf(output, "  path:       %s\n", v.Path)
	fmt.Fprintf(output, "  path state: %s\n", pathState(v))
	fmt.Fprintf(output, "  profile:    %s\n", v.Profile)
	fmt.Fprintf(output, "  registered: %s\n", v.RegisteredAt)
	fmt.Fprintf(output, "  manifest:   %s\n", manifestState(v))
	for _, warning := range v.Warnings {
		fmt.Fprintf(output, "  warning:    %s\n", warning)
	}
	return nil
}

func pathState(v projectView) string {
	if v.PathAvailable {
		return "available"
	}
	return "unavailable"
}

func manifestState(v projectView) string {
	switch {
	case v.ManifestPresent && v.ManifestLocal:
		return projectmanifest.FileName + " with a local override"
	case v.ManifestPresent:
		return projectmanifest.FileName
	case v.ManifestLocal:
		return projectmanifest.LocalFileName + " only"
	default:
		return "none"
	}
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
