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
	"github.com/lev-goryachev/lctk/internal/nvidiainstall"
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
		{args: []string{"inspect", ContainerName, "--format", `{{.State.Status}}|{{.Image}}|{{index .Config.Labels "tech.lctk.inference-config"}}|{{index .Config.Labels "tech.lctk.inference-distribution"}}`}, stdout: "running|sha256:test|" + ConfigRevision + "|cpu\n"},
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

func TestSelfTestUsesItsLongerEmbeddingClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(10 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": make([]float64, Dimensions)}},
		})
	}))
	t.Cleanup(server.Close)
	manager := NewManagerForTest(nil, "image@sha256:test", writeTestModel(t), server.URL)
	manager.healthClient = &http.Client{Timeout: time.Millisecond}
	manager.selfTestClient = &http.Client{Timeout: 100 * time.Millisecond}
	if err := manager.SelfTest(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestHealthRejectsHTTP200WhileModelIsStillLoading(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "loading model"})
	}))
	t.Cleanup(server.Close)
	manager := NewManagerForTest(nil, "image@sha256:test", writeTestModel(t), server.URL)
	if err := manager.health(t.Context()); err == nil || !strings.Contains(err.Error(), "loading model") {
		t.Fatalf("health error = %v, want loading state", err)
	}
}

func TestProductionHealthUsesThePrivateContainerAddressThroughMachineTunnel(t *testing.T) {
	model := writeTestModel(t)
	health := healthyServer(t)
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{stdout: "sha256:test\n"},
		{stdout: "running|sha256:test|" + ConfigRevision + "|cpu\n"},
		{args: []string{"inspect", ContainerName, "--format", `{{(index .NetworkSettings.Networks "podman").IPAddress}}`}, stdout: "10.88.0.2\n"},
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

func TestStatusUsesTheImmutableImageIDInsteadOfNormalizedReferenceText(t *testing.T) {
	model := writeTestModel(t)
	health := healthyServer(t)
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{args: []string{"image", "inspect", "registry/image:tag@sha256:test", "--format", "{{.Id}}"}, stdout: "sha256:local-id\n"},
		{args: []string{"inspect", ContainerName, "--format", `{{.State.Status}}|{{.Image}}|{{index .Config.Labels "tech.lctk.inference-config"}}|{{index .Config.Labels "tech.lctk.inference-distribution"}}`}, stdout: "running|sha256:local-id|" + ConfigRevision + "|cpu\n"},
	}}
	manager := NewManagerForTest(runner, "registry/image:tag@sha256:test", model, health.URL)
	status, err := manager.Status(t.Context())
	if err != nil || !status.Ready {
		t.Fatalf("Status = %+v, %v; want the matching immutable image ID to be ready", status, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("remaining runtime calls = %d", len(runner.calls))
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
		{stderr: "No such container", err: errors.New("exit 1")},
		{stderr: "No such container", err: errors.New("exit 1")},
		{stdout: "candidate-id\n"},
		{args: []string{"rename", CandidateContainerName, ContainerName}},
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
	if !status.Ready || len(runner.seen) != 6 {
		t.Fatalf("status = %+v, calls = %v", status, runner.seen)
	}
	run := strings.Join(runner.seen[4], " ")
	for _, required := range []string{
		"run --detach --name " + CandidateContainerName,
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

func TestNVIDIACandidateRequiresDeviceOffloadAndMeasuredBackend(t *testing.T) {
	model := writeTestModel(t)
	server := healthyServer(t)
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{stdout: "sha256:gpu\n"},
		{stderr: "No such container", err: errors.New("exit 1")},
		{stderr: "No such container", err: errors.New("exit 1")},
		{stderr: "No such container", err: errors.New("exit 1")},
		{stdout: "candidate\n"},
		{args: []string{"exec", CandidateContainerName, "nvidia-smi", "--query-gpu=name,driver_version,memory.total,compute_cap", "--format=csv,noheader,nounits"}, stdout: "NVIDIA GeForce GTX 1070, 582.53, 8192, 6.1\n"},
		{args: []string{"inspect", CandidateContainerName, "--format", "{{.State.StartedAt}}"}, stdout: "2026-08-07 00:07:42.409325154 +0200 CEST\n"},
		{args: []string{"logs", "--since", "2026-08-07T00:07:42.409325154+02:00", "--until", "2026-08-07T00:09:42.409325154+02:00", CandidateContainerName}, stderr: "ggml_cuda_init: found 1 CUDA devices\nload_tensors: offloaded 13/13 layers to GPU\nCUDA0 compute buffer"},
		{args: []string{"rename", CandidateContainerName, ContainerName}},
		{args: []string{"exec", ContainerName, "nvidia-smi", "--query-gpu=name,driver_version,memory.total,compute_cap", "--format=csv,noheader,nounits"}, stdout: "NVIDIA GeForce GTX 1070, 582.53, 8192, 6.1\n"},
		{args: []string{"inspect", ContainerName, "--format", "{{.State.StartedAt}}"}, stdout: "2026-08-07 00:07:42.409325154 +0200 CEST\n"},
		{args: []string{"logs", "--since", "2026-08-07T00:07:42.409325154+02:00", "--until", "2026-08-07T00:09:42.409325154+02:00", ContainerName}, stderr: "ggml_cuda_init: found 1 CUDA devices\nload_tensors: offloaded 13/13 layers to GPU\nCUDA0 compute buffer"},
	}}
	manager := NewManagerForTest(runner, nvidiainstall.Image, model, server.URL)
	manager.distribution = DistributionNVIDIAGPU
	status, err := manager.Ensure(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Backend != "cuda" || status.GPU == nil || status.OffloadedLayers != "13/13" {
		t.Fatalf("unexpected GPU status: %+v", status)
	}
	run := strings.Join(runner.seen[4], " ")
	for _, wanted := range []string{
		"--device " + nvidiainstall.CDIDevice,
		"--n-gpu-layers 99",
		"tech.lctk.inference-distribution=nvidia_gpu",
	} {
		if !strings.Contains(run, wanted) {
			t.Errorf("GPU candidate args omit %q: %s", wanted, run)
		}
	}
}

func TestNVIDIABackendRejectsPartialLayerOffload(t *testing.T) {
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{stdout: "NVIDIA GeForce GTX 1070, 582.53, 8192, 6.1\n"},
		{stdout: "2026-08-07 00:07:42.409325154 +0200 CEST\n"},
		{stdout: "CUDA0\nload_tensors: offloaded 12/13 layers to GPU\n"},
	}}
	manager := NewManagerForTest(runner, nvidiainstall.Image, writeTestModel(t), "http://127.0.0.1:1")
	manager.distribution = DistributionNVIDIAGPU
	err := manager.attachBackendEvidence(t.Context(), CandidateContainerName, &Status{})
	if !nvidiainstall.IsCode(err, nvidiainstall.FailureCUDAOffloadMissing) {
		t.Fatalf("partial offload error = %v, want CUDA offload failure", err)
	}
}

func TestCandidateSwapPreservesProjectAliasAndDeletesRollbackOnlyAfterProof(t *testing.T) {
	model := writeTestModel(t)
	server := healthyServer(t)
	topology := `{"podman":{"Aliases":["0123456789ab"]},"lctk-project-net":{"Aliases":["lctk-inference","abcdef012345"]}}`
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{stdout: "sha256:new\n"},
		{stdout: "running|sha256:old|6|cpu\n"},
		{stderr: "No such container", err: errors.New("exit 1")},
		{stderr: "No such container", err: errors.New("exit 1")},
		{stdout: "candidate\n"},
		{args: []string{"inspect", ContainerName, "--format", `{{json .NetworkSettings.Networks}}`}, stdout: topology},
		{args: []string{"rename", ContainerName, RollbackContainerName}},
		{args: []string{"network", "disconnect", "lctk-project-net", RollbackContainerName}},
		{args: []string{"rename", CandidateContainerName, ContainerName}},
		{args: []string{"network", "connect", "--alias", ContainerName, "lctk-project-net", ContainerName}},
		{args: []string{"rm", "--force", RollbackContainerName}},
	}}
	manager := NewManagerForTest(runner, "image@sha256:new", model, server.URL)
	status, err := manager.Ensure(t.Context(), time.Second)
	if err != nil || !status.Ready {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unconsumed runtime calls: %+v", runner.calls)
	}
}

func TestCandidateSwapFailureRestoresOldNameAndAlias(t *testing.T) {
	model := writeTestModel(t)
	server := healthyServer(t)
	topology := `{"podman":{"Aliases":["0123456789ab"]},"lctk-project-net":{"Aliases":["lctk-inference","abcdef012345"]}}`
	runner := &scriptedRunner{t: t, calls: []runnerCall{
		{stdout: "sha256:new\n"},
		{stdout: "running|sha256:old|6|cpu\n"},
		{stderr: "No such container", err: errors.New("exit 1")},
		{stderr: "No such container", err: errors.New("exit 1")},
		{stdout: "candidate\n"},
		{stdout: topology},
		{},
		{},
		{},
		{stderr: "forced network error", err: errors.New("exit 125")},
		{args: []string{"rm", "--force", ContainerName}},
		{args: []string{"network", "connect", "--alias", ContainerName, "lctk-project-net", RollbackContainerName}},
		{args: []string{"rename", RollbackContainerName, ContainerName}},
	}}
	manager := NewManagerForTest(runner, "image@sha256:new", model, server.URL)
	_, err := manager.Ensure(t.Context(), time.Second)
	if !errors.Is(err, ErrSwapFailed) {
		t.Fatalf("error=%v want ErrSwapFailed", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("rollback omitted calls: %+v", runner.calls)
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
		switch r.URL.Path {
		case "/health":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "/v1/embeddings":
			vector := make([]float64, Dimensions)
			if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": vector}}}); err != nil {
				t.Errorf("encode self-test response: %v", err)
			}
		default:
			t.Errorf("unexpected inference path = %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
