package inference

import (
	"bytes"
	"context"
	"os/exec"
)

type dockerRunner struct{}

func (dockerRunner) Run(ctx context.Context, args ...string) (string, string, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

// NewDockerManager constructs the pinned production lifecycle over the local
// Docker CLI.
func NewDockerManager() (*Manager, error) { return NewManager(dockerRunner{}) }
