package windowssetup

import (
	"errors"
	"testing"
)

func TestWorkingWSLProvesVirtualizationWhenWindowsHidesTheFirmwareFlag(t *testing.T) {
	status, err := evaluateHost(Status{Build: MinimumBuild, Architecture: "amd64", WSLReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || status.RequiresEnablement {
		t.Fatalf("status=%+v, want a ready supported host", status)
	}
}

func TestFirmwareCapabilityAllowsWSLEnablement(t *testing.T) {
	status, err := evaluateHost(Status{Build: MinimumBuild, Architecture: "amd64", VirtualizationFirmware: true})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.RequiresEnablement {
		t.Fatalf("status=%+v, want a supported host requiring WSL enablement", status)
	}
}

func TestHostEvaluationDistinguishesPlatformAndVirtualizationFailures(t *testing.T) {
	for name, status := range map[string]Status{
		"old-build":          {Build: MinimumBuild - 1, Architecture: "amd64", VirtualizationFirmware: true},
		"wrong-architecture": {Build: MinimumBuild, Architecture: "arm64", VirtualizationFirmware: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := evaluateHost(status); !errors.Is(err, ErrUnsupportedHost) {
				t.Fatalf("error=%v, want %v", err, ErrUnsupportedHost)
			}
		})
	}

	status := Status{Build: MinimumBuild, Architecture: "amd64"}
	if _, err := evaluateHost(status); !errors.Is(err, ErrVirtualizationUnavailable) {
		t.Fatalf("error=%v, want %v", err, ErrVirtualizationUnavailable)
	}
}
