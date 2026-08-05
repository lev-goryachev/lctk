package runtimeapi

import (
	"context"
	"errors"
	"testing"
)

type probeRunner struct {
	stdout string
	stderr string
	err    error
}

func (r probeRunner) Run(context.Context, ...string) (string, string, error) {
	return r.stdout, r.stderr, r.err
}

func TestProbeReportsManagedLinuxIdentity(t *testing.T) {
	status, err := ProbeWithRunner(t.Context(), probeRunner{stdout: `{"host":{"os":"linux"},"version":{"Version":"5.8.2"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Available || status.Provider != "podman" || status.Version != "5.8.2" || status.OSType != "linux" {
		t.Fatalf("status = %+v", status)
	}
}

func TestProbeRejectsUnavailableAndNonLinuxRuntimes(t *testing.T) {
	if _, err := ProbeWithRunner(t.Context(), probeRunner{stderr: "machine stopped", err: errors.New("exit")}); err == nil {
		t.Fatal("unavailable runtime was accepted")
	}
	if _, err := ProbeWithRunner(t.Context(), probeRunner{stdout: `{"host":{"os":"windows"}}`}); err == nil {
		t.Fatal("non-Linux runtime was accepted")
	}
}
