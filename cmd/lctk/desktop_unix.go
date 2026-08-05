//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

// startBackgroundDaemon detaches the process group used by the source-build
// desktop path so it survives the short-lived launcher process.
func startBackgroundDaemon(executable string) error {
	command := exec.Command(executable, "daemon")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start LCTK daemon: %w", err)
	}
	return command.Process.Release()
}
