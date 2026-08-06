package inference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/containerruntime"
)

type runnerCall struct {
	args   []string
	stdout string
	stderr string
	err    error
}

type scriptedRunner struct {
	t     *testing.T
	calls []runnerCall
	seen  [][]string
}

type tunnelStub struct {
	address string
	remote  string
}

func (s *tunnelStub) Ensure(_ context.Context, _ string, remote string) (string, error) {
	s.remote = remote
	return s.address, nil
}

func (r *scriptedRunner) Run(_ context.Context, args ...string) (string, string, error) {
	r.t.Helper()
	r.seen = append(r.seen, append([]string(nil), args...))
	if len(r.calls) == 0 {
		r.t.Fatalf("unexpected runtime call: %v", args)
	}
	call := r.calls[0]
	r.calls = r.calls[1:]
	if call.args != nil && !reflect.DeepEqual(args, call.args) {
		r.t.Fatalf("runtime args = %v, want %v", args, call.args)
	}
	return call.stdout, call.stderr, call.err
}

func TestEnsureReusesThePinnedHealthyContainer(t *testing.T) {
	model := writeTestModel(t)
	health := healthyServer(t)
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{args: []string{"image", "inspect", "image@sha256:test", "--format", "{{.Id}}"}, stdout: "sha256:test\n"},
		{args: []string{"inspect", ContainerName, "--format", `{{.State.Status}}|{{.Config.Image}}|{{index .Config.Labels "tech.lctk.inference-config"}}`}, stdout: "running|image@sha256:test|" + ConfigRevision + "\n"},
	}}
	manager := NewManagerForTest(runner, "image@sha256:test", model, health.URL)
	status, err := manager.Ensure(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !status.Ready || len(runner.calls) != 0 {
		t.Fatalf("status = %+v, remaining calls = %d", status, len(runner.calls))
	}
}

func TestProductionHealthUsesThePrivateContainerAddressThroughMachineTunnel(t *testing.T) {
	model := writeTestModel(t)
	health := healthyServer(t)
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{stdout: "sha256:test\n"},
		{stdout: "running|image@sha256:test|" + ConfigRevision + "\n"},
		{args: []string{"inspect", ContainerName, "--format", `{{(index .NetworkSettings.Networks "podman").IPAddress}}`}, stdout: "10.88.0.2\n"},
	}}
	manager := NewManagerForTest(runner, "image@sha256:test", model, health.URL)
	manager.address = ""
	tunnel := &tunnelStub{address: strings.TrimPrefix(health.URL, "http://")}
	manager.tunnel = tunnel
	status, err := manager.Ensure(t.Context(), time.Second)
	if err != nil || !status.Ready {
		t.Fatalf("Ensure status=%+v err=%v", status, err)
	}
	if tunnel.remote != "10.88.0.2:8080" {
		t.Fatalf("machine tunnel remote=%q", tunnel.remote)
	}
}

func TestProductionSelfTestReusesThePrivateContainerMachineTunnel(t *testing.T) {
	model := writeTestModel(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/embeddings" {
			t.Errorf("self-test path=%q", request.URL.Path)
		}
		vector := make([]float64, Dimensions)
		if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": vector}}}); err != nil {
			t.Errorf("encode self-test response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	format := `{{(index .NetworkSettings.Networks "podman").IPAddress}}`
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{args: []string{"inspect", ContainerName, "--format", format}, stdout: "10.88.0.2\n"},
	}}
	manager := NewManagerForTest(runner, "image@sha256:test", model, "")
	tunnel := &tunnelStub{address: strings.TrimPrefix(server.URL, "http://")}
	manager.tunnel = tunnel
	if err := manager.SelfTest(t.Context()); err != nil {
		t.Fatalf("SelfTest: %v", err)
	}
	if tunnel.remote != "10.88.0.2:8080" {
		t.Fatalf("machine tunnel remote=%q", tunnel.remote)
	}
}

func TestEnsureStartsOneLoopbackOnlyStatelessContainer(t *testing.T) {
	model := writeTestModel(t)
	health := healthyServer(t)
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{stdout: "sha256:test\n"},
		{stderr: "No such container", err: errors.New("exit 1")},
		{stdout: "container-id\n"},
	}}
	manager := NewManagerForTest(runner, "image@sha256:test", model, health.URL)
	runtimeModel, err := containerruntime.HostPath(model)
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Ensure(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !status.Ready || len(runner.seen) != 3 {
		t.Fatalf("status = %+v, calls = %v", status, runner.seen)
	}
	run := strings.Join(runner.seen[2], " ")
	for _, required := range []string{
		"run --detach --name " + ContainerName,
		"source=" + runtimeModel,
		"target=/models/" + ModelName + ",readonly",
		"--embedding --pooling mean",
		"--parallel 8",
		"--batch-size 4096 --ubatch-size 4096",
	} {
		if !strings.Contains(run, required) {
			t.Errorf("run args are missing %q: %s", required, run)
		}
	}
	if strings.Contains(run, "--publish") {
		t.Errorf("inference run exposes a WSL port instead of using the authenticated machine tunnel: %s", run)
	}
}

func TestVerifyModelRejectsAnUnpinnedFile(t *testing.T) {
	path := writeTestModel(t)
	err := VerifyModel(path)
	if !errors.Is(err, ErrModelInvalid) {
		t.Fatalf("VerifyModel error = %v, want ErrModelInvalid", err)
	}
}

func TestCancelledInstallStartsNoDownloadOrRuntimeOperation(t *testing.T) {
	runner := &scriptedRunner{t: t}
	manager := NewManagerForTest(runner, "image@sha256:test", filepath.Join(t.TempDir(), "missing.gguf"), "http://127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.PullImage(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("PullImage error = %v, want context cancellation", err)
	}
	if err := manager.InstallModel(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("InstallModel error = %v, want context cancellation", err)
	}
	if len(runner.seen) != 0 {
		t.Fatalf("cancelled install executed runtime calls: %v", runner.seen)
	}
}

func writeTestModel(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(path, []byte("test model bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func healthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("health path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}
