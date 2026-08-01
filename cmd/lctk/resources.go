package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/lev-goryachev/lctk/internal/diskspace"
	"github.com/lev-goryachev/lctk/internal/hostsettings"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

type resourcesView struct {
	ProjectID string `json:"project_id"`
	// MachineMode is the policy in force for every project without its own.
	MachineMode string `json:"machine_mode"`
	// ProjectMode is this project's override, empty when it follows the machine.
	ProjectMode string `json:"project_mode,omitempty"`
	// EffectiveMode is what actually applies.
	EffectiveMode string `json:"effective_mode"`
	// CPUs, MemoryLimitMB, and IndexParallelism are what the mode costs. Zero
	// means no limit in each case.
	CPUs             float64 `json:"cpus"`
	MemoryLimitMB    int     `json:"memory_limit_mb"`
	IndexParallelism int     `json:"index_parallelism"`
	// RestartRequired says the running container predates this policy, because
	// limits are applied when a container is created.
	RestartRequired bool `json:"restart_required"`
	// Disk is what the project costs and what room is left for it.
	Disk diskView `json:"disk"`
}

// runProjectResources shows or sets how much of the machine a project may use.
//
// The mode lives in the registry rather than in the repository manifest. How much
// of someone's machine a project may spend is theirs to decide, and a repository
// author has no say in it.
func runProjectResources(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("project resources", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "write the result as JSON")
	mode := flags.String("mode", "", "set this project's mode: quiet, normal, fast, or default to follow the machine")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: lctk project resources [--mode quiet|normal|fast|default] [--json] PROJECT")
	}

	registry, err := projectregistry.Load()
	if err != nil {
		return err
	}
	project, err := registry.Resolve(flags.Arg(0))
	if err != nil {
		return err
	}

	changed := false
	if *mode != "" {
		requested := strings.ToLower(strings.TrimSpace(*mode))
		if requested == "default" {
			requested = ""
		}
		if !hostsettings.Mode(requested).Valid() {
			return fmt.Errorf("unknown resource mode %q: expected quiet, normal, fast, or default", *mode)
		}
		if project.ResourceMode != requested {
			if err := registry.SetResourceMode(project.ID, requested); err != nil {
				return err
			}
			if err := registry.Save(); err != nil {
				return err
			}
			project.ResourceMode = requested
			changed = true
		}
	}

	settings, err := hostsettings.Load()
	if err != nil {
		return err
	}
	effective := settings.Resources.WithProjectMode(hostsettings.Mode(project.ResourceMode))
	budget := effective.Budget()

	estimate, estimateErr := estimateFor(context.Background(), project)
	if estimateErr != nil {
		// A missing figure is not a reason to withhold the policy the caller asked
		// about. It is reported as unknown by being left at zero.
		estimate = diskspace.Estimate{}
	}

	view := resourcesView{
		ProjectID:        project.ID,
		MachineMode:      string(settings.Resources.Mode),
		ProjectMode:      project.ResourceMode,
		EffectiveMode:    string(effective.Mode),
		CPUs:             budget.CPUs,
		MemoryLimitMB:    budget.MemoryLimitMB,
		IndexParallelism: budget.IndexParallelism,
		RestartRequired:  changed,
		Disk:             diskViewOf(estimate),
	}
	if *asJSON {
		return writeJSON(stdout, view)
	}

	fmt.Fprintf(stdout, "%s\n", view.ProjectID)
	fmt.Fprintf(stdout, "  mode:        %s", view.EffectiveMode)
	if view.ProjectMode == "" {
		fmt.Fprintf(stdout, " (from the machine policy)\n")
	} else {
		fmt.Fprintf(stdout, " (set for this project; the machine is %s)\n", view.MachineMode)
	}
	fmt.Fprintf(stdout, "  cpus:        %s\n", limitOrUnlimited(view.CPUs))
	fmt.Fprintf(stdout, "  memory:      %s\n", memoryOrUnlimited(view.MemoryLimitMB))
	fmt.Fprintf(stdout, "  index work:  %s\n", parallelismOrEngine(view.IndexParallelism))
	printDisk(stdout, view.Disk)
	if view.RestartRequired {
		fmt.Fprintf(stdout, "  note:        limits apply when a container is created; "+
			"run lctk project restart %s to apply this now\n", view.ProjectID)
	}
	return nil
}

func limitOrUnlimited(cpus float64) string {
	if cpus <= 0 {
		return "no limit"
	}
	return fmt.Sprintf("%.6g", cpus)
}

func memoryOrUnlimited(megabytes int) string {
	if megabytes <= 0 {
		return "no limit"
	}
	return fmt.Sprintf("%d MB", megabytes)
}

func parallelismOrEngine(parallelism int) string {
	if parallelism <= 0 {
		return "as many as the container allows"
	}
	return fmt.Sprintf("%d at a time", parallelism)
}
