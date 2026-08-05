package runtimeinstall

import (
	"bytes"
	"context"

	"github.com/lev-goryachev/lctk/internal/containerruntime"
)

type machineCLI struct{}

func (machineCLI) Run(ctx context.Context, args ...string) (string, string, error) {
	command, err := containerruntime.MachineCommand(ctx, args...)
	if err != nil {
		return "", "", err
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	return stdout.String(), stderr.String(), err
}
