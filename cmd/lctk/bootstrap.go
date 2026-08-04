package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/lev-goryachev/lctk/internal/inference"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

type bootstrapInference interface {
	ImageAvailable(context.Context) bool
	ModelAvailable() bool
	PullImage(context.Context) error
	InstallModel(context.Context, *http.Client) error
	Ensure(context.Context, time.Duration) (inference.Status, error)
	SelfTest(context.Context) error
}

var newBootstrapInference = func() (bootstrapInference, error) {
	return inference.NewDockerManager()
}

type bootstrapComponent struct {
	Name          string `json:"name"`
	Identity      string `json:"identity"`
	Installed     bool   `json:"installed"`
	DownloadBytes int64  `json:"download_bytes,omitempty"`
}

type bootstrapPlan struct {
	Version           string               `json:"version"`
	OS                string               `json:"os"`
	Arch              string               `json:"arch"`
	Writes            bool                 `json:"writes"`
	Ready             bool                 `json:"ready"`
	Components        []bootstrapComponent `json:"components"`
	DiskBytes         int64                `json:"disk_bytes"`
	Applied           bool                 `json:"applied"`
	SelfTest          bool                 `json:"self_test"`
	RecommendedAction string               `json:"recommended_action,omitempty"`
}

func runBootstrap(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	planOnly := flags.Bool("plan", false, "inspect prerequisites and planned writes without changing state")
	proceed := flags.Bool("yes", false, "apply the displayed bootstrap plan")
	asJSON := flags.Bool("json", false, "write the plan and result as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: lctk bootstrap [--plan] [--yes] [--json]")
	}

	stack := newStackManager()
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := stack.RuntimeAvailable(probeCtx); err != nil {
		return err
	}
	shared, err := newBootstrapInference()
	if err != nil {
		return err
	}
	codeImage := projectstack.ImageRepository + ":" + buildinfo.Version
	codeInstalled := stack.ImageAvailable(probeCtx, codeImage) == nil
	inferenceInstalled := shared.ImageAvailable(probeCtx)
	modelInstalled := shared.ModelAvailable()
	plan := bootstrapPlan{
		Version: buildinfo.Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Writes: false, Ready: codeInstalled && inferenceInstalled && modelInstalled,
		Components: []bootstrapComponent{
			{Name: "code-intel", Identity: codeImage, Installed: codeInstalled},
			{Name: "embedding-inference", Identity: inference.Image, Installed: inferenceInstalled},
			{Name: "embedding-model", Identity: inference.ModelSHA256, Installed: modelInstalled,
				DownloadBytes: missingBytes(modelInstalled, inference.ModelBytes)},
		},
		DiskBytes: missingBytes(modelInstalled, inference.ModelBytes),
	}
	if !codeInstalled {
		plan.RecommendedAction = "Install the matching code-intel image; in a source checkout run lctk image build."
	}
	if *planOnly || !*proceed {
		if *asJSON {
			return writeJSON(stdout, plan)
		}
		writeBootstrapPlan(stdout, plan)
		if !*planOnly {
			fmt.Fprintln(stdout, "No changes applied. Review the plan, then run lctk bootstrap --yes.")
		}
		return nil
	}
	if !codeInstalled {
		if *asJSON {
			_ = writeJSON(stdout, plan)
		}
		return errors.New(plan.RecommendedAction)
	}
	applyCtx, applyCancel := context.WithTimeout(ctx, 20*time.Minute)
	defer applyCancel()
	if !inferenceInstalled {
		if err := shared.PullImage(applyCtx); err != nil {
			return err
		}
	}
	if !modelInstalled {
		if err := shared.InstallModel(applyCtx, nil); err != nil {
			return err
		}
	}
	if _, err := shared.Ensure(applyCtx, 2*time.Minute); err != nil {
		return err
	}
	if err := shared.SelfTest(applyCtx); err != nil {
		return err
	}
	plan.Writes = true
	plan.Ready = true
	plan.Applied = true
	plan.SelfTest = true
	for index := range plan.Components {
		plan.Components[index].Installed = true
		plan.Components[index].DownloadBytes = 0
	}
	plan.DiskBytes = 0
	plan.RecommendedAction = ""
	if *asJSON {
		return writeJSON(stdout, plan)
	}
	fmt.Fprintln(stdout, "Bootstrap complete; the pinned local embedding path passed its functional self-test.")
	return nil
}

func missingBytes(installed bool, size int64) int64 {
	if installed {
		return 0
	}
	return size
}

func writeBootstrapPlan(output io.Writer, plan bootstrapPlan) {
	fmt.Fprintf(output, "LCTK %s bootstrap plan for %s/%s (writes: %t)\n", plan.Version, plan.OS, plan.Arch, plan.Writes)
	for _, component := range plan.Components {
		fmt.Fprintf(output, "  %s: installed=%t identity=%s", component.Name, component.Installed, component.Identity)
		if component.DownloadBytes > 0 {
			fmt.Fprintf(output, " download=%d bytes", component.DownloadBytes)
		}
		fmt.Fprintln(output)
	}
	if plan.RecommendedAction != "" {
		fmt.Fprintln(output, "  action:", plan.RecommendedAction)
	}
}
