package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/lev-goryachev/lctk/internal/daemon"
	"github.com/lev-goryachev/lctk/internal/projectgrant"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

const grantUsage = `Usage:
  lctk grant show [--json] [--reveal] PROJECT
  lctk grant list [--json]
  lctk grant revoke [--json] GRANT_ID

A grant is issued automatically when a project is registered. The token is
withheld unless --reveal is given, so it does not end up in a shared terminal
transcript by accident.
`

// grantView is the machine-readable shape of a grant. The token is present only
// when the caller explicitly asked to reveal it.
type grantView struct {
	ID         string   `json:"id"`
	Client     string   `json:"client"`
	ProjectIDs []string `json:"project_ids"`
	IssuedAt   string   `json:"issued_at"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	Revoked    bool     `json:"revoked,omitempty"`
	Endpoint   string   `json:"endpoint,omitempty"`
	Token      string   `json:"token,omitempty"`
	// TokenEnvVar names the environment variable a client should read the token
	// from. Slice 0.4 measured that Codex refuses an inline token, so the
	// variable name is part of the contract rather than a convenience.
	TokenEnvVar string `json:"token_env_var,omitempty"`
}

func grantViewOf(grant projectgrant.Grant, projectID string, reveal bool) grantView {
	view := grantView{
		ID:         grant.ID,
		Client:     grant.Client,
		ProjectIDs: grant.ProjectIDs,
		IssuedAt:   grant.IssuedAt.Format(time.RFC3339),
		Revoked:    grant.Revoked,
	}
	if !grant.ExpiresAt.IsZero() {
		view.ExpiresAt = grant.ExpiresAt.Format(time.RFC3339)
	}
	if projectID != "" {
		view.Endpoint = fmt.Sprintf("http://%s/projects/%s/mcp", daemon.DefaultAddress, projectID)
		view.TokenEnvVar = projectgrant.EnvVarName(projectID)
	}
	if reveal {
		view.Token = grant.Token
	}
	return view
}

func runGrant(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, grantUsage)
		return errors.New("a grant subcommand is required")
	}
	switch args[0] {
	case "show":
		return runGrantShow(args[1:], stdout)
	case "list":
		return runGrantList(args[1:], stdout)
	case "revoke":
		return runGrantRevoke(args[1:], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, grantUsage)
		return nil
	default:
		fmt.Fprint(stderr, grantUsage)
		return fmt.Errorf("unknown grant subcommand %q", args[0])
	}
}

func runGrantShow(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("grant show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the grant as JSON")
	reveal := flags.Bool("reveal", false, "include the token value in the output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk grant show [--json] [--reveal] PROJECT")
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}
	project, err := registry.Resolve(flags.Arg(0))
	if err != nil {
		return err
	}

	grants, err := projectgrant.Load()
	if err != nil {
		return err
	}
	grant, err := grants.ForProject(project.ID, time.Now())
	if err != nil {
		// A project registered before grants existed, or whose grant was revoked,
		// gets one now rather than failing.
		grant, err = grants.EnsureForProject(project.ID, projectgrant.DefaultClient, time.Now())
		if err != nil {
			return err
		}
		if err := grants.Save(); err != nil {
			return err
		}
	}

	view := grantViewOf(grant, project.ID, *reveal)
	if *asJSON {
		return writeJSON(stdout, view)
	}
	fmt.Fprintf(stdout, "%s\n", view.ID)
	fmt.Fprintf(stdout, "  client:   %s\n", view.Client)
	fmt.Fprintf(stdout, "  projects: %v\n", view.ProjectIDs)
	fmt.Fprintf(stdout, "  endpoint: %s\n", view.Endpoint)
	fmt.Fprintf(stdout, "  env var:  %s\n", view.TokenEnvVar)
	if *reveal {
		fmt.Fprintf(stdout, "  token:    %s\n", view.Token)
	} else {
		fmt.Fprint(stdout, "  token:    hidden, pass --reveal to print it\n")
	}
	return nil
}

func runGrantList(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("grant list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write grants as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: lctk grant list [--json]")
	}

	grants, err := projectgrant.Load()
	if err != nil {
		return err
	}
	list := grants.List()
	views := make([]grantView, 0, len(list))
	for _, grant := range list {
		// Listing never reveals tokens; there is no reason to print every secret
		// to see what exists.
		views = append(views, grantViewOf(grant, "", false))
	}

	if *asJSON {
		return writeJSON(stdout, views)
	}
	if len(views) == 0 {
		fmt.Fprint(stdout, "No grants have been issued.\n")
		return nil
	}
	for _, view := range views {
		state := "active"
		if view.Revoked {
			state = "revoked"
		}
		fmt.Fprintf(stdout, "%s  %s  %s  %v\n", view.ID, state, view.Client, view.ProjectIDs)
	}
	return nil
}

func runGrantRevoke(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("grant revoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the revoked grant as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk grant revoke [--json] GRANT_ID")
	}

	grants, err := projectgrant.Load()
	if err != nil {
		return err
	}
	grant, err := grants.Revoke(flags.Arg(0))
	if err != nil {
		return err
	}
	if err := grants.Save(); err != nil {
		return err
	}

	view := grantViewOf(grant, "", false)
	if *asJSON {
		return writeJSON(stdout, view)
	}
	fmt.Fprintf(stdout, "Revoked %s\n", view.ID)
	fmt.Fprint(stdout, "Clients using this token will be refused on their next request.\n")
	return nil
}
