package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/internal/commandpolicy"
	"github.com/lev-goryachev/lctk/internal/projectmanifest"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

type commandsView struct {
	ProjectID string `json:"project_id"`
	// Image is the container approved commands run in, empty when none is set,
	// which is also the reason nothing can run.
	Image    string                 `json:"image,omitempty"`
	Network  string                 `json:"network"`
	Commands []commandpolicy.Status `json:"commands"`
	// Warnings repeats what the manifest parser reported, so a typo in the
	// manifest is visible at the moment somebody is deciding what to approve.
	Warnings []string `json:"warnings,omitempty"`
}

// runProjectCommands shows and changes what a project is allowed to run.
//
// Approval is the act this command exists for, and it is deliberately a human
// one. The repository proposes a command; a person reads the exact text and
// agrees to it; only then can a client run it, and only by name.
func runProjectCommands(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("project commands", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the result as JSON")
	approve := flags.String("approve", "", "approve the manifest's build, test, or lint command as it stands now")
	revoke := flags.String("revoke", "", "withdraw approval for build, test, or lint")
	image := flags.String("image", "", "the container image approved commands run in")
	network := flags.String("network", "", "none or full network access for approved commands")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk project commands [--approve NAME] [--revoke NAME] " +
			"[--image IMAGE] [--network none|full] [--json] PROJECT")
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}
	project, err := registry.Resolve(flags.Arg(0))
	if err != nil {
		return err
	}

	manifest, manifestErr := projectmanifest.Load(project.Path)
	if manifestErr != nil {
		return fmt.Errorf("read the project manifest: %w", manifestErr)
	}
	proposals := []commandpolicy.Proposal{
		{Name: commandpolicy.NameBuild, Command: manifest.Manifest.Commands.Build},
		{Name: commandpolicy.NameTest, Command: manifest.Manifest.Commands.Test},
		{Name: commandpolicy.NameLint, Command: manifest.Manifest.Commands.Lint},
	}

	set := project.Commands
	changed := false

	if *image != "" {
		set.Image = strings.TrimSpace(*image)
		changed = true
	}
	if *network != "" {
		policy := strings.ToLower(strings.TrimSpace(*network))
		if !commandpolicy.ValidNetwork(policy) {
			return fmt.Errorf("unknown network policy %q: expected none or full", *network)
		}
		set.Network = policy
		changed = true
	}
	if *revoke != "" {
		if !set.Revoke(strings.ToLower(strings.TrimSpace(*revoke))) {
			return fmt.Errorf("%q was not approved, so there is nothing to withdraw", *revoke)
		}
		changed = true
	}
	if *approve != "" {
		name := strings.ToLower(strings.TrimSpace(*approve))
		command := ""
		for _, proposal := range proposals {
			if proposal.Name == name {
				command = strings.TrimSpace(proposal.Command)
			}
		}
		if command == "" {
			return fmt.Errorf(
				"the project's manifest proposes no %s command; add commands.%s to %s first",
				name, name, projectmanifest.FileName)
		}
		if err := set.Approve(name, command, time.Now()); err != nil {
			return err
		}
		changed = true
	}

	if changed {
		if err := registry.SetCommands(project.ID, set); err != nil {
			return err
		}
		if err := registry.Save(); err != nil {
			return err
		}
	}

	view := commandsView{
		ProjectID: project.ID,
		Image:     set.Image,
		Network:   set.NetworkOrDefault(),
		Commands:  set.Describe(proposals),
		Warnings:  manifest.Warnings,
	}
	if *asJSON {
		return writeJSON(stdout, view)
	}
	return printCommands(stdout, view)
}

func printCommands(stdout io.Writer, view commandsView) error {
	fmt.Fprintf(stdout, "%s\n", view.ProjectID)
	if view.Image == "" {
		fmt.Fprintf(stdout, "  image:    none, so nothing can run; set one with --image\n")
	} else {
		fmt.Fprintf(stdout, "  image:    %s\n", view.Image)
	}
	fmt.Fprintf(stdout, "  network:  %s\n", view.Network)

	for _, status := range view.Commands {
		fmt.Fprintf(stdout, "  %-6s   %s\n", status.Name, describeCommand(status, view.Image))
		if status.Command != "" {
			fmt.Fprintf(stdout, "            %s\n", status.Command)
		}
	}
	for _, warning := range view.Warnings {
		fmt.Fprintf(stdout, "  warning:  %s\n", warning)
	}
	return nil
}

// describeCommand says which of the several things that must be true is not.
func describeCommand(status commandpolicy.Status, image string) string {
	switch {
	case !status.Proposed:
		return "not proposed by the manifest"
	case status.Changed:
		return "CHANGED since it was approved; review it and approve it again"
	case !status.Approved:
		return "proposed, not approved"
	case image == "":
		return "approved, but the project has no image to run it in"
	default:
		return "runnable, approved " + status.ApprovedAt.Format(time.RFC3339)
	}
}
