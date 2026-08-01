package projectstack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/lev-goryachev/lctk/internal/hostsettings"
	"github.com/lev-goryachev/lctk/internal/projectregistry"
)

// ErrInvalidProject reports a project that cannot be turned into a stack.
var ErrInvalidProject = errors.New("project cannot be used as a container stack")

// The Compose document is modelled with structs rather than maps so that the
// rendered bytes are deterministic. Go marshals struct fields in declaration
// order, whereas map iteration order is unspecified, and Slice 1.2 requires that
// generated configuration be reproducible.

type composeFile struct {
	Name     string          `yaml:"name"`
	Services composeServices `yaml:"services"`
	Networks composeNetworks `yaml:"networks"`
	Volumes  composeVolumes  `yaml:"volumes"`
}

type composeServices struct {
	CodeIntel composeService `yaml:"code-intel"`
}

type composeService struct {
	Image         string `yaml:"image"`
	ContainerName string `yaml:"container_name"`
	Init          bool   `yaml:"init"`
	Restart       string `yaml:"restart"`
	// CPUs and MemLimit carry the background-load policy into the runtime. Both
	// are omitted when unset, so a project with no limits renders exactly the
	// document it rendered before limits existed.
	CPUs        float64            `yaml:"cpus,omitempty"`
	MemLimit    string             `yaml:"mem_limit,omitempty"`
	Environment []string           `yaml:"environment"`
	Volumes     []composeMount     `yaml:"volumes"`
	Ports       []string           `yaml:"ports"`
	Networks    []string           `yaml:"networks"`
	Healthcheck composeHealthcheck `yaml:"healthcheck"`
	Labels      []string           `yaml:"labels"`
}

// composeMount uses Compose long syntax deliberately. Short syntax separates
// fields with colons, which is ambiguous for a Windows path such as C:\work, so
// long syntax is a correctness requirement on the primary host platform rather
// than a style preference.
type composeMount struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty"`
}

type composeHealthcheck struct {
	Test        []string `yaml:"test"`
	Interval    string   `yaml:"interval"`
	Timeout     string   `yaml:"timeout"`
	Retries     int      `yaml:"retries"`
	StartPeriod string   `yaml:"start_period"`
}

type composeNetworks struct {
	Project composeNetwork `yaml:"project"`
}

type composeNetwork struct {
	Name   string `yaml:"name"`
	Driver string `yaml:"driver"`
}

type composeVolumes struct {
	State composeVolume `yaml:"state"`
}

type composeVolume struct {
	Name string `yaml:"name"`
}

// Render produces the Compose document for one project.
//
// The output is a pure function of the project record, the resource budget, and
// the product version, so rendering the same inputs twice yields identical bytes.
// Nothing time-based, random, or environment-dependent may enter this function --
// which is why the budget is a parameter rather than something read from the
// settings file here.
func Render(project projectregistry.Project, budget hostsettings.Budget) ([]byte, error) {
	names, err := DeriveNames(project.ID)
	if err != nil {
		return nil, err
	}
	if project.Path == "" {
		return nil, fmt.Errorf("%w: project %s has no host path", ErrInvalidProject, project.ID)
	}
	if !filepath.IsAbs(project.Path) {
		return nil, fmt.Errorf("%w: host path %q is not absolute", ErrInvalidProject, project.Path)
	}

	document := composeFile{
		Name: names.ComposeName,
		Services: composeServices{
			CodeIntel: composeService{
				Image:         names.Image,
				ContainerName: names.ContainerName,
				Init:          true,
				// The stack is started and stopped explicitly by LCTK, so a
				// container must not come back on its own after a stop.
				Restart:     "no",
				CPUs:        budget.CPUs,
				MemLimit:    memoryLimit(budget.MemoryLimitMB),
				Environment: environment(project, budget),
				Volumes: []composeMount{
					{
						Type:   "bind",
						Source: project.Path,
						Target: WorkspaceMount,
						// The code-intel boundary never writes to the working
						// tree. A writable mount belongs to the future runner.
						ReadOnly: true,
					},
					{
						Type:   "volume",
						Source: "state",
						Target: StateMount,
					},
				},
				// The published port has no number on the host side, so the
				// runtime assigns a free one and two projects can never collide.
				// It is bound to loopback: the project service is reachable from
				// this machine's daemon and from nowhere else. Short syntax is
				// unambiguous here because a port specification contains no paths.
				Ports:    []string{"127.0.0.1::" + strconv.Itoa(ServicePort)},
				Networks: []string{"project"},
				Healthcheck: composeHealthcheck{
					Test:        []string{"CMD", "test", "-f", StateMount + "/ready"},
					Interval:    "5s",
					Timeout:     "3s",
					Retries:     6,
					StartPeriod: "2s",
				},
				Labels: []string{
					"tech.lctk.project-id=" + project.ID,
					"tech.lctk.managed=true",
					"tech.lctk.version=" + buildinfo.Version,
				},
			},
		},
		Networks: composeNetworks{
			Project: composeNetwork{Name: names.Network, Driver: "bridge"},
		},
		Volumes: composeVolumes{
			State: composeVolume{Name: names.Volume},
		},
	}

	body, err := yaml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("render compose for %s: %w", project.ID, err)
	}

	header := "# Generated by LCTK. Do not edit: this file is rewritten from the\n" +
		"# registry whenever the project starts, and the registry is authoritative.\n"
	return append([]byte(header), body...), nil
}

// environment is what the project service reads at startup.
//
// The parallelism cap is passed in rather than derived inside the container,
// because the container cannot see the host's policy and would otherwise size
// itself to the whole machine regardless of what the operator asked for.
func environment(project projectregistry.Project, budget hostsettings.Budget) []string {
	values := []string{
		"LCTK_PROJECT_ID=" + project.ID,
		"LCTK_PROJECT_PROFILE=" + string(project.Profile),
		"LCTK_WORKSPACE=" + WorkspaceMount,
		"LCTK_STATE_DIR=" + StateMount,
	}
	if budget.IndexParallelism > 0 {
		values = append(values, "LCTK_INDEX_PARALLELISM="+strconv.Itoa(budget.IndexParallelism))
	}
	return values
}

func memoryLimit(megabytes int) string {
	if megabytes <= 0 {
		return ""
	}
	return strconv.Itoa(megabytes) + "m"
}

// Write renders the Compose document and stores it under the LCTK home.
//
// The write is atomic so an interrupted start cannot leave a half-written file
// that Compose would later refuse or, worse, misread.
func Write(project projectregistry.Project, budget hostsettings.Budget) (string, error) {
	body, err := Render(project, budget)
	if err != nil {
		return "", err
	}

	dir, err := StackDir(project.ID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create stack directory %q: %w", dir, err)
	}

	path := filepath.Join(dir, "compose.yaml")
	temp, err := os.CreateTemp(dir, "compose.*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary compose file in %q: %w", dir, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if _, err := temp.Write(body); err != nil {
		temp.Close()
		return "", fmt.Errorf("write temporary compose file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", fmt.Errorf("flush temporary compose file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close temporary compose file: %w", err)
	}
	if err := os.Chmod(tempName, 0o600); err != nil {
		return "", fmt.Errorf("restrict temporary compose file permissions: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return "", fmt.Errorf("replace compose file %q: %w", path, err)
	}
	return path, nil
}
