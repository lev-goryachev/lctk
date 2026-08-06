package nvidiainstall

import (
	"bytes"
	"context"
	"io"

	"github.com/lev-goryachev/lctk/internal/containerruntime"
)

// machineTransport enters only the LCTK-owned machine as its configured root
// user. Every remote executable and script is supplied as an argument; no host
// shell or ambient WSL distribution participates.
type machineTransport struct{}

func (machineTransport) Run(ctx context.Context, args ...string) (string, string, error) {
	return runMachine(ctx, nil, args...)
}

func (machineTransport) RunInput(ctx context.Context, input io.Reader, args ...string) (string, string, error) {
	return runMachine(ctx, input, args...)
}

func runMachine(ctx context.Context, input io.Reader, args ...string) (string, string, error) {
	selected := append([]string{"ssh", containerruntime.MachineName}, args...)
	command, err := containerruntime.MachineCommand(ctx, selected...)
	if err != nil {
		return "", "", err
	}
	var stdout, stderr bytes.Buffer
	command.Stdin = input
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	return stdout.String(), stderr.String(), err
}
