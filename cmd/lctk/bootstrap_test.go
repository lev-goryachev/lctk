package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/inference"
	"github.com/lev-goryachev/lctk/internal/projectstack"
)

type bootstrapRunner struct {
	codeImage bool
}

func (r bootstrapRunner) Run(_ context.Context, args ...string) (string, string, error) {
	if len(args) > 0 && args[0] == "version" {
		return "29.0 linux\n", "", nil
	}
	if len(args) > 1 && args[0] == "image" && args[1] == "inspect" && r.codeImage {
		return "sha256:code\n", "", nil
	}
	return "", "missing", context.Canceled
}

type bootstrapInferenceStub struct {
	image, model                       bool
	pulled, installed, ensured, tested bool
}

func (s *bootstrapInferenceStub) ImageAvailable(context.Context) bool { return s.image }
func (s *bootstrapInferenceStub) ModelAvailable() bool                { return s.model }
func (s *bootstrapInferenceStub) PullImage(context.Context) error {
	s.pulled = true
	s.image = true
	return nil
}
func (s *bootstrapInferenceStub) InstallModel(context.Context, *http.Client) error {
	s.installed = true
	s.model = true
	return nil
}
func (s *bootstrapInferenceStub) Ensure(context.Context, time.Duration) (inference.Status, error) {
	s.ensured = true
	return inference.Status{Ready: true}, nil
}
func (s *bootstrapInferenceStub) SelfTest(context.Context) error {
	s.tested = true
	return nil
}

func TestBootstrapWithoutConfirmationIsAReadOnlyPlan(t *testing.T) {
	stub := &bootstrapInferenceStub{}
	restoreBootstrapFactories(t, bootstrapRunner{codeImage: true}, stub)
	var output bytes.Buffer
	if err := runBootstrap(t.Context(), nil, &output); err != nil {
		t.Fatalf("runBootstrap: %v", err)
	}
	if stub.pulled || stub.installed || stub.ensured || stub.tested {
		t.Fatalf("read-only plan mutated state: %+v", stub)
	}
	for _, wanted := range []string{"writes: false", "No changes applied", inference.ModelSHA256} {
		if !strings.Contains(output.String(), wanted) {
			t.Errorf("plan is missing %q:\n%s", wanted, output.String())
		}
	}
}

func TestConfirmedBootstrapInstallsAndFunctionallyTestsMissingComponents(t *testing.T) {
	stub := &bootstrapInferenceStub{}
	restoreBootstrapFactories(t, bootstrapRunner{codeImage: true}, stub)
	var output bytes.Buffer
	if err := runBootstrap(t.Context(), []string{"--yes", "--json"}, &output); err != nil {
		t.Fatalf("runBootstrap: %v", err)
	}
	if !stub.pulled || !stub.installed || !stub.ensured || !stub.tested {
		t.Fatalf("applied bootstrap did not complete every gate: %+v", stub)
	}
	for _, wanted := range []string{`"applied": true`, `"self_test": true`, `"ready": true`} {
		if !strings.Contains(output.String(), wanted) {
			t.Errorf("result is missing %q:\n%s", wanted, output.String())
		}
	}
}

func restoreBootstrapFactories(t *testing.T, runner projectstack.Runner, shared bootstrapInference) {
	t.Helper()
	oldStack := newStackManager
	oldInference := newBootstrapInference
	newStackManager = func() *projectstack.Manager { return projectstack.NewManagerWithRunner(runner) }
	newBootstrapInference = func() (bootstrapInference, error) { return shared, nil }
	t.Cleanup(func() {
		newStackManager = oldStack
		newBootstrapInference = oldInference
	})
}
