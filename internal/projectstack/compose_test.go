package projectstack

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/lev-goryachev/lctk/internal/hostsettings"
	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(lctkhome.EnvOverride, home)
	return home
}

// absPath builds an absolute host path valid on the running platform. A leading
// separator is absolute on POSIX but not on Windows, where a volume is required,
// and Render rejects a non-absolute path.
func absPath(parts ...string) string {
	joined := filepath.Join(parts...)
	if runtime.GOOS == "windows" {
		return filepath.Join(`C:\`, joined)
	}
	return filepath.Join("/", joined)
}

// testBudget is the shipped normal-mode policy, so the tests exercise the
// document a real project renders rather than a limit-free special case.
var testBudget = hostsettings.Resources{Mode: hostsettings.ModeNormal}.Budget()

func testProject(id, path string) projectregistry.Project {
	return projectregistry.Project{
		ID:           id,
		Name:         filepath.Base(path),
		Path:         path,
		Key:          strings.ToLower(filepath.ToSlash(path)),
		Profile:      projectregistry.ProfileMinimal,
		RegisteredAt: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
	}
}

func TestDeriveNamesIsStableAndPrefixed(t *testing.T) {
	names, err := DeriveNames("alpha-abcd1234")
	if err != nil {
		t.Fatal(err)
	}
	if names.ComposeName != "lctk-alpha-abcd1234" {
		t.Errorf("compose name = %q", names.ComposeName)
	}
	if names.Network != "lctk-alpha-abcd1234-net" {
		t.Errorf("network = %q", names.Network)
	}
	if names.Volume != "lctk-alpha-abcd1234-state" {
		t.Errorf("volume = %q", names.Volume)
	}
	if names.ContainerName != "lctk-alpha-abcd1234-code-intel" {
		t.Errorf("container = %q", names.ContainerName)
	}
	// The image tag follows the product version so an upgrade does not silently
	// reuse an older image.
	if names.Image != ImageRepository+":"+buildinfo.Version {
		t.Errorf("image = %q", names.Image)
	}

	again, err := DeriveNames("alpha-abcd1234")
	if err != nil {
		t.Fatal(err)
	}
	if again != names {
		t.Error("DeriveNames is not deterministic")
	}
}

func TestDeriveNamesRejectsUnusableIdentifiers(t *testing.T) {
	for _, id := range []string{"", "Upper", "has space", "-leading", "semi;colon", "../escape"} {
		if _, err := DeriveNames(id); !errors.Is(err, ErrInvalidProject) {
			t.Errorf("DeriveNames(%q) = %v, want ErrInvalidProject", id, err)
		}
	}
}

func TestRenderIsReproducible(t *testing.T) {
	project := testProject("alpha-abcd1234", absPath("work", "alpha"))

	first, err := Render(project, testBudget)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := Render(project, testBudget)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("render %d differs:\n--- first ---\n%s\n--- again ---\n%s", i, first, again)
		}
	}
}

func TestRenderProducesTheExpectedStack(t *testing.T) {
	hostPath := absPath("work", "alpha")
	project := testProject("alpha-abcd1234", hostPath)

	body, err := Render(project, testBudget)
	if err != nil {
		t.Fatal(err)
	}

	// Parse rather than string-match, so the assertions describe the stack the
	// runtime will actually see.
	var document struct {
		Name     string `yaml:"name"`
		Services struct {
			CodeIntel struct {
				Image         string   `yaml:"image"`
				ContainerName string   `yaml:"container_name"`
				Restart       string   `yaml:"restart"`
				Environment   []string `yaml:"environment"`
				Networks      []string `yaml:"networks"`
				Labels        []string `yaml:"labels"`
				Volumes       []struct {
					Type     string `yaml:"type"`
					Source   string `yaml:"source"`
					Target   string `yaml:"target"`
					ReadOnly bool   `yaml:"read_only"`
				} `yaml:"volumes"`
				Healthcheck struct {
					Test []string `yaml:"test"`
				} `yaml:"healthcheck"`
			} `yaml:"code-intel"`
		} `yaml:"services"`
		Networks struct {
			Project struct {
				Name string `yaml:"name"`
			} `yaml:"project"`
		} `yaml:"networks"`
		Volumes struct {
			State struct {
				Name string `yaml:"name"`
			} `yaml:"state"`
		} `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(body, &document); err != nil {
		t.Fatalf("generated compose is not valid YAML: %v\n%s", err, body)
	}

	service := document.Services.CodeIntel
	if document.Name != "lctk-alpha-abcd1234" {
		t.Errorf("compose project name = %q", document.Name)
	}
	if document.Networks.Project.Name != "lctk-alpha-abcd1234-net" {
		t.Errorf("network name = %q", document.Networks.Project.Name)
	}
	if document.Volumes.State.Name != "lctk-alpha-abcd1234-state" {
		t.Errorf("volume name = %q", document.Volumes.State.Name)
	}
	if service.Restart != "no" {
		t.Errorf("restart = %q, want no so a stop stays stopped", service.Restart)
	}

	if len(service.Volumes) != 2 {
		t.Fatalf("expected a source bind and a state volume, got %+v", service.Volumes)
	}
	bind := service.Volumes[0]
	if bind.Type != "bind" {
		t.Errorf("first mount type = %q, want bind", bind.Type)
	}
	if bind.Source != hostPath {
		t.Errorf("bind source = %q, want %q", bind.Source, hostPath)
	}
	if bind.Target != WorkspaceMount {
		t.Errorf("bind target = %q", bind.Target)
	}
	// The code-intel boundary must never be able to write to the working tree.
	if !bind.ReadOnly {
		t.Error("the source bind mount is not read-only")
	}
	state := service.Volumes[1]
	if state.Type != "volume" || state.Target != StateMount {
		t.Errorf("state mount = %+v", state)
	}

	if len(service.Healthcheck.Test) == 0 {
		t.Error("no healthcheck was generated")
	}
	if !containsString(service.Environment, "LCTK_PROJECT_ID="+project.ID) {
		t.Errorf("environment does not carry the project id: %v", service.Environment)
	}
	if !containsString(service.Labels, "tech.lctk.project-id="+project.ID) {
		t.Errorf("labels do not carry the project id: %v", service.Labels)
	}
}

// TestLongMountSyntaxPreservesAColonInTheSource covers the reason long syntax is
// mandatory: a Windows drive letter contains a colon, which Compose short syntax
// uses as its field separator, so `C:\work:/workspace:ro` is ambiguous.
//
// The mount is marshalled directly rather than through Render, because a Windows
// path is not absolute on a POSIX host and Render rightly rejects it. The
// property under test is the encoding, which is platform-independent.
func TestLongMountSyntaxPreservesAColonInTheSource(t *testing.T) {
	const windowsSource = `C:\work\alpha`
	body, err := yaml.Marshal(composeMount{
		Type:     "bind",
		Source:   windowsSource,
		Target:   WorkspaceMount,
		ReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var decoded composeMount
	if err := yaml.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("mount does not round-trip: %v\n%s", err, body)
	}
	if decoded.Source != windowsSource {
		t.Errorf("source = %q, want %q", decoded.Source, windowsSource)
	}
	if decoded.Target != WorkspaceMount {
		t.Errorf("target = %q", decoded.Target)
	}
	if !decoded.ReadOnly {
		t.Error("read-only flag was lost")
	}

	// The encoding must be a mapping with named fields, never a single
	// colon-separated scalar.
	if !strings.Contains(string(body), "source:") || !strings.Contains(string(body), "target:") {
		t.Errorf("mount was not encoded in long syntax:\n%s", body)
	}
}

func TestRenderRejectsAnUnusableProject(t *testing.T) {
	if _, err := Render(testProject("alpha-abcd1234", ""), testBudget); !errors.Is(err, ErrInvalidProject) {
		t.Errorf("empty path: got %v", err)
	}
	if _, err := Render(testProject("alpha-abcd1234", "relative/path"), testBudget); !errors.Is(err, ErrInvalidProject) {
		t.Errorf("relative path: got %v", err)
	}
	if _, err := Render(testProject("Bad ID", `C:\work\alpha`), testBudget); !errors.Is(err, ErrInvalidProject) {
		t.Errorf("bad id: got %v", err)
	}
}

// TestTwoProjectsGetSeparateResources is the roadmap's required isolation check.
func TestTwoProjectsGetSeparateResources(t *testing.T) {
	alpha := testProject("alpha-aaaaaaaa", absPath("work", "alpha"))
	beta := testProject("beta-bbbbbbbb", absPath("work", "beta"))

	alphaNames, err := DeriveNames(alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	betaNames, err := DeriveNames(beta.ID)
	if err != nil {
		t.Fatal(err)
	}

	if alphaNames.ComposeName == betaNames.ComposeName {
		t.Error("two projects share a Compose project name")
	}
	if alphaNames.Network == betaNames.Network {
		t.Error("two projects share a network")
	}
	if alphaNames.Volume == betaNames.Volume {
		t.Error("two projects share a volume")
	}
	if alphaNames.ContainerName == betaNames.ContainerName {
		t.Error("two projects share a container name")
	}
	// The reusable image is deliberately shared, per ADR-0003.
	if alphaNames.Image != betaNames.Image {
		t.Error("projects should share the reusable image")
	}

	alphaBody, err := Render(alpha, testBudget)
	if err != nil {
		t.Fatal(err)
	}
	betaBody, err := Render(beta, testBudget)
	if err != nil {
		t.Fatal(err)
	}
	// Neither stack may mention the other project's source path or resources.
	if strings.Contains(string(alphaBody), beta.Path) || strings.Contains(string(alphaBody), betaNames.Volume) {
		t.Error("alpha's stack references beta")
	}
	if strings.Contains(string(betaBody), alpha.Path) || strings.Contains(string(betaBody), alphaNames.Volume) {
		t.Error("beta's stack references alpha")
	}
}

func TestWriteStoresOutsideTheRepositoryAndIsAtomic(t *testing.T) {
	home := isolate(t)
	project := testProject("alpha-abcd1234", absPath("work", "alpha"))

	path, err := Write(project, testBudget)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, home) {
		t.Errorf("compose file %q is outside the LCTK home %q", path, home)
	}

	expected, err := ComposeFilePath(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if path != expected {
		t.Errorf("path = %q, want %q", path, expected)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := Render(project, testBudget)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(rendered) {
		t.Error("written file does not match Render output")
	}
	if !strings.HasPrefix(string(body), "# Generated by LCTK") {
		t.Errorf("file lacks the generated-file warning:\n%s", body)
	}

	// Rewriting must leave no temporary files behind.
	for i := 0; i < 3; i++ {
		if _, err := Write(project, testBudget); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("expected only compose.yaml, found %d entries", len(entries))
	}
}

func TestStackDirRejectsAnUnusableIdentifier(t *testing.T) {
	isolate(t)
	for _, id := range []string{"", "..", "../escape", "Upper"} {
		if _, err := StackDir(id); !errors.Is(err, ErrInvalidProject) {
			t.Errorf("StackDir(%q) = %v, want ErrInvalidProject", id, err)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// A resource mode has to reach the runtime, not just the settings file. The
// rendered document is where that becomes real.
func TestTheResourceModeReachesTheRenderedDocument(t *testing.T) {
	project := testProject("alpha-abcd1234", absPath("work", "alpha"))

	quiet := hostsettings.Resources{Mode: hostsettings.ModeQuiet}.Budget()
	body, err := Render(project, quiet)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(body)
	if !strings.Contains(rendered, "cpus: 1") {
		t.Errorf("quiet mode did not limit CPU:\n%s", rendered)
	}
	if !strings.Contains(rendered, "LCTK_INDEX_PARALLELISM=1") {
		t.Errorf("quiet mode did not bound index work:\n%s", rendered)
	}
	if strings.Contains(rendered, "mem_limit") {
		t.Errorf("a memory limit appeared without being asked for:\n%s", rendered)
	}

	fast := hostsettings.Resources{Mode: hostsettings.ModeFast}.Budget()
	body, err = Render(project, fast)
	if err != nil {
		t.Fatal(err)
	}
	rendered = string(body)
	if strings.Contains(rendered, "cpus:") {
		t.Errorf("fast mode still limited CPU:\n%s", rendered)
	}
	if strings.Contains(rendered, "LCTK_INDEX_PARALLELISM") {
		t.Errorf("fast mode still bounded index work:\n%s", rendered)
	}

	capped := hostsettings.Resources{Mode: hostsettings.ModeNormal, MemoryLimitMB: 1536}.Budget()
	body, err = Render(project, capped)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "mem_limit: 1536m") {
		t.Errorf("an explicit memory limit did not reach the document:\n%s", body)
	}
}

// Reproducibility has to survive the new parameter: the same project and the same
// budget must render the same bytes.
func TestRenderingStaysReproducibleAcrossModes(t *testing.T) {
	project := testProject("alpha-abcd1234", absPath("work", "alpha"))
	for _, mode := range []hostsettings.Mode{hostsettings.ModeQuiet, hostsettings.ModeNormal, hostsettings.ModeFast} {
		budget := hostsettings.Resources{Mode: mode}.Budget()
		first, err := Render(project, budget)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Render(project, budget)
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(again) {
			t.Fatalf("mode %q rendered different bytes twice", mode)
		}
	}
}
