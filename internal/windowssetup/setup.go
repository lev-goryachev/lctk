// Package windowssetup owns the Windows/WSL prerequisite boundary for the
// one-click installer. Platform mutations are never performed by a plan call.
package windowssetup

import "errors"

const MinimumBuild = 19045

var (
	// ErrUnsupportedHost reports a host outside the accepted Windows 10 22H2
	// amd64 installation contract.
	ErrUnsupportedHost = errors.New("setup requires Windows 10 22H2 build 19045 or newer on amd64")
	// ErrVirtualizationUnavailable reports a host where neither the firmware
	// capability nor an already operational WSL2 runtime proves that LCTK can
	// create its managed Linux machine.
	ErrVirtualizationUnavailable = errors.New("setup requires hardware virtualization enabled in firmware or an already working WSL2 runtime")
	// ErrElevationRequired reports prerequisite mutation attempted without UAC
	// elevation.
	ErrElevationRequired = errors.New("administrator approval is required to enable WSL2 prerequisites")
)

// Status is the read-only prerequisite evidence displayed before installation.
type Status struct {
	Build                  uint32 `json:"build"`
	Architecture           string `json:"architecture"`
	VirtualizationFirmware bool   `json:"virtualization_firmware"`
	WSLReady               bool   `json:"wsl_ready"`
	Elevated               bool   `json:"elevated"`
	Supported              bool   `json:"supported"`
	RequiresEnablement     bool   `json:"requires_enablement"`
}

// evaluateHost applies the accepted host contract to facts gathered without
// mutation. A working WSL2 runtime is stronger evidence than the firmware flag:
// Windows can hide that flag after Hyper-V owns the virtualization extensions.
func evaluateHost(status Status) (Status, error) {
	if status.Build < MinimumBuild || status.Architecture != "amd64" {
		return status, ErrUnsupportedHost
	}
	if !status.VirtualizationFirmware && !status.WSLReady {
		return status, ErrVirtualizationUnavailable
	}
	status.Supported = true
	status.RequiresEnablement = !status.WSLReady
	return status, nil
}
