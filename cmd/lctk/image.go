package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

const imageUsage = `Usage:
  lctk image build [--context DIR] [--json]
  lctk image status [--json]

One reusable image is shared by every project, per ADR-0003, and its tag follows
the product version, per ADR-0007. Until images are published, the image is built
locally from the repository build context.
`

// defaultImageContext is the in-repository build context. It is a development
// affordance: published images arrive with release automation in Stage 7.
const defaultImageContext = "images/code-intel"

type imageView struct {
	Image     string `json:"image"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
	Context   string `json:"context,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

func runImage(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, imageUsage)
		return errors.New("an image subcommand is required")
	}
	switch args[0] {
	case "build":
		return runImageBuild(args[1:], stdout)
	case "status":
		return runImageStatus(args[1:], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, imageUsage)
		return nil
	default:
		fmt.Fprint(stderr, imageUsage)
		return fmt.Errorf("unknown image subcommand %q", args[0])
	}
}

// reusableImage is the tag every project stack refers to.
func reusableImage() string {
	return projectstack.ImageRepository + ":" + buildinfo.Version
}

func runImageBuild(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("image build", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	contextDir := flags.String("context", defaultImageContext, "build context directory")
	asJSON := flags.Bool("json", false, "write the result as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: lctk image build [--context DIR] [--json]")
	}

	absolute, err := filepath.Abs(*contextDir)
	if err != nil {
		return err
	}

	image := reusableImage()
	manager := newStackManager()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	if err := manager.RuntimeAvailable(ctx); err != nil {
		return err
	}
	if err := manager.BuildImage(ctx, image, absolute); err != nil {
		return err
	}

	view := imageView{Image: image, Version: buildinfo.Version, Available: true, Context: absolute}
	if *asJSON {
		return writeJSON(stdout, view)
	}
	fmt.Fprintf(stdout, "Built %s from %s\n", image, absolute)
	return nil
}

func runImageStatus(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("image status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the result as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: lctk image status [--json]")
	}

	image := reusableImage()
	view := imageView{Image: image, Version: buildinfo.Version}

	manager := newStackManager()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Availability is reported rather than raised, because "the image is missing"
	// is the answer to this question, not a failure of the command.
	if err := manager.RuntimeAvailable(ctx); err != nil {
		view.Detail = err.Error()
	} else if err := manager.ImageAvailable(ctx, image); err != nil {
		view.Detail = "build it with lctk image build"
	} else {
		view.Available = true
	}

	if *asJSON {
		return writeJSON(stdout, view)
	}
	fmt.Fprintf(stdout, "%s\n", image)
	fmt.Fprintf(stdout, "  available: %t\n", view.Available)
	if view.Detail != "" {
		fmt.Fprintf(stdout, "  detail:    %s\n", view.Detail)
	}
	return nil
}
