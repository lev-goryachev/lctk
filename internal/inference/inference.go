// Package inference manages the one installation-wide embedding service.
//
// The service is stateless compute. Project text and vectors never enter its
// container filesystem: each project owns those in its isolated state volume.
package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/internal/containerruntime"

	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/machinetunnel"
)

const (
	ContainerName = "lctk-inference"
	ContainerPort = 8080
	ModelName     = "nomic-embed-text-v1.5.Q4_K_M.gguf"
	ModelAlias    = ModelName
	ModelSHA256   = "d4e388894e09cf3816e8b0896d81d265b55e7a9fff9ab03fe8bf4ef5e11295ac"
	ModelBytes    = int64(84106624)
	ModelURL      = "https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF/resolve/0188c9bf409793f810680a5a431e7b899c46104c/" + ModelName
	Image         = "ghcr.io/ggml-org/llama.cpp:server@sha256:14ab06c571008509adcedf635301edfa98071b1b8345269921d31ea4d519ae47"
	Dimensions    = 768
	// ConfigRevision forces replacement when runtime arguments change while the
	// immutable image and model remain the same.
	ConfigRevision = "6"

	// ProjectEndpoint is reached after the inference container joins the one
	// requesting project's isolated network. No Windows host port is involved.
	ProjectEndpoint = "http://lctk-inference:8080/v1/embeddings"
	// RuntimeNetwork is Podman's rootful default network. The shared inference
	// container remains attached to it while also serving isolated projects.
	RuntimeNetwork = "podman"
)

var (
	ErrImageMissing = errors.New("embedding inference image is not installed")
	ErrModelMissing = errors.New("embedding model is not installed")
	ErrModelInvalid = errors.New("embedding model digest does not match")
	ErrNotReady     = errors.New("embedding inference service is not ready")
)

// Runner executes OCI runtime operations and makes lifecycle behavior testable.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout string, stderr string, err error)
}

type tunnel interface {
	Ensure(context.Context, string, string) (string, error)
}

// Manager owns the shared container lifecycle. Configuration fields are private
// so production always uses pinned identities; tests use NewManagerForTest.
type Manager struct {
	runner      Runner
	image       string
	modelPath   string
	modelBytes  int64
	modelSHA    string
	downloadURL string
	address     string
	httpClient  *http.Client
	tunnel      tunnel
}

// Status is the observable shared inference state.
type Status struct {
	Ready     bool   `json:"ready"`
	Container string `json:"container"`
	Image     string `json:"image"`
	Model     string `json:"model"`
	ModelPath string `json:"model_path"`
	Endpoint  string `json:"endpoint"`
	Detail    string `json:"detail,omitempty"`
}

// ModelPath returns the installation-owned model location without creating it.
func ModelPath() (string, error) {
	home, err := lctkhome.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "models", ModelName), nil
}

// NewManager constructs the production manager from immutable component ids.
func NewManager(runner Runner) (*Manager, error) {
	path, err := ModelPath()
	if err != nil {
		return nil, err
	}
	return &Manager{runner: runner, image: Image, modelPath: path, modelBytes: ModelBytes,
		modelSHA: ModelSHA256, downloadURL: ModelURL, httpClient: &http.Client{Timeout: 2 * time.Second},
		tunnel: machinetunnel.Default}, nil
}

// NewManagerForTest supplies isolated endpoints and artifacts without weakening
// production constants.
func NewManagerForTest(runner Runner, image, modelPath, address string) *Manager {
	size, digest := fileIdentity(modelPath)
	return &Manager{runner: runner, image: image, modelPath: modelPath,
		modelBytes: size, modelSHA: digest, downloadURL: ModelURL, address: address,
		httpClient: &http.Client{Timeout: 2 * time.Second}}
}

// VerifyModel streams the whole file through SHA-256 and checks size first. A
// filename or successful download is never accepted as model identity.
func VerifyModel(path string) error {
	return verifyFile(path, ModelBytes, ModelSHA256)
}

func verifyFile(path string, wantedBytes int64, wantedSHA string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrModelMissing, path)
		}
		return fmt.Errorf("inspect embedding model %q: %w", path, err)
	}
	if info.Size() != wantedBytes {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrModelInvalid, info.Size(), wantedBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open embedding model %q: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash embedding model %q: %w", path, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != wantedSHA {
		return fmt.Errorf("%w: got %s, want %s", ErrModelInvalid, got, wantedSHA)
	}
	return nil
}

// Ensure starts the pinned service only after both image and model identities
// are locally verifiable, then waits for the real HTTP health path.
func (m *Manager) Ensure(ctx context.Context, wait time.Duration) (Status, error) {
	status := m.baseStatus()
	if m.runner == nil {
		return status, errors.New("embedding inference runner is not configured")
	}
	if _, stderr, err := m.runner.Run(ctx, "image", "inspect", m.image, "--format", "{{.Id}}"); err != nil {
		status.Detail = firstLine(stderr, err)
		return status, fmt.Errorf("%w: %s", ErrImageMissing, status.Detail)
	}
	if err := verifyFile(m.modelPath, m.modelBytes, m.modelSHA); err != nil {
		status.Detail = err.Error()
		return status, err
	}

	format := `{{.State.Status}}|{{.Config.Image}}|{{index .Config.Labels "tech.lctk.inference-config"}}`
	stdout, _, inspectErr := m.runner.Run(ctx, "inspect", ContainerName, "--format", format)
	fields := strings.Split(strings.TrimSpace(stdout), "|")
	runningCurrent := inspectErr == nil && len(fields) == 3 && fields[0] == "running" &&
		fields[1] == m.image && fields[2] == ConfigRevision
	if !runningCurrent {
		if inspectErr == nil || strings.TrimSpace(stdout) != "" {
			if _, stderr, err := m.runner.Run(ctx, "rm", "--force", ContainerName); err != nil {
				return status, fmt.Errorf("replace embedding inference container: %s", firstLine(stderr, err))
			}
		}
		runtimeModelPath, err := containerruntime.HostPath(m.modelPath)
		if err != nil {
			return status, fmt.Errorf("prepare embedding model mount: %w", err)
		}
		mount := "type=bind,source=" + runtimeModelPath + ",target=/models/" + ModelName + ",readonly"
		args := []string{
			"run", "--detach", "--name", ContainerName, "--restart", "unless-stopped",
			"--label", "tech.lctk.managed=true", "--label", "tech.lctk.component=inference",
			"--label", "tech.lctk.inference-config=" + ConfigRevision,
			"--mount", mount, m.image,
			"--model", "/models/" + ModelName, "--alias", ModelAlias,
			"--embedding", "--pooling", "mean", "--host", "0.0.0.0",
			"--parallel", "8",
			"--port", fmt.Sprint(ContainerPort), "--ctx-size", "32768",
			"--batch-size", "4096", "--ubatch-size", "4096",
		}
		if _, stderr, err := m.runner.Run(ctx, args...); err != nil {
			return status, fmt.Errorf("start embedding inference: %s", firstLine(stderr, err))
		}
	}
	if wait <= 0 {
		wait = 90 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		if err := m.health(ctx); err == nil {
			status.Ready = true
			return status, nil
		} else {
			status.Detail = err.Error()
		}
		if time.Now().After(deadline) {
			return status, fmt.Errorf("%w: %s", ErrNotReady, status.Detail)
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Status checks installed identities, container state, and the live endpoint
// without changing any of them.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	status := m.baseStatus()
	if err := verifyFile(m.modelPath, m.modelBytes, m.modelSHA); err != nil {
		status.Detail = err.Error()
		return status, err
	}
	format := `{{.State.Status}}|{{.Config.Image}}|{{index .Config.Labels "tech.lctk.inference-config"}}`
	stdout, stderr, err := m.runner.Run(ctx, "inspect", ContainerName, "--format", format)
	if err != nil {
		status.Detail = firstLine(stderr, err)
		return status, fmt.Errorf("inspect embedding inference: %s", status.Detail)
	}
	fields := strings.Split(strings.TrimSpace(stdout), "|")
	if len(fields) != 3 || fields[0] != "running" || fields[1] != m.image || fields[2] != ConfigRevision {
		status.Detail = "The pinned embedding container is not running."
		return status, ErrNotReady
	}
	if err := m.health(ctx); err != nil {
		status.Detail = err.Error()
		return status, err
	}
	status.Ready = true
	return status, nil
}

func fileIdentity(path string) (int64, string) {
	file, err := os.Open(path)
	if err != nil {
		return ModelBytes, ModelSHA256
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return ModelBytes, ModelSHA256
	}
	return size, hex.EncodeToString(hash.Sum(nil))
}

func (m *Manager) baseStatus() Status {
	endpoint := ProjectEndpoint
	if m.address != "" {
		endpoint = m.address + "/v1/embeddings"
	}
	return Status{Container: ContainerName, Image: m.image, Model: ModelAlias,
		ModelPath: m.modelPath, Endpoint: endpoint}
}

func (m *Manager) health(ctx context.Context) error {
	address := m.address
	if m.tunnel != nil {
		format := fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, RuntimeNetwork)
		stdout, stderr, err := m.runner.Run(ctx, "inspect", ContainerName, "--format", format)
		if err != nil {
			return fmt.Errorf("inspect embedding container address: %s", firstLine(stderr, err))
		}
		containerAddress := strings.TrimSpace(stdout)
		if containerAddress == "" {
			return fmt.Errorf("embedding container has no address on runtime network %s", RuntimeNetwork)
		}
		remote := net.JoinHostPort(containerAddress, fmt.Sprint(ContainerPort))
		local, err := m.tunnel.Ensure(ctx, "inference", remote)
		if err != nil {
			return fmt.Errorf("open embedding machine tunnel: %w", err)
		}
		address = "http://" + local
	}
	if address == "" {
		return errors.New("embedding health endpoint is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/health", nil)
	if err != nil {
		return err
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("embedding health returned %s", response.Status)
	}
	return nil
}

func firstLine(stderr string, err error) string {
	if line := strings.TrimSpace(strings.SplitN(stderr, "\n", 2)[0]); line != "" {
		return line
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}
