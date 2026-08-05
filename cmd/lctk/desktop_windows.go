//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// startBackgroundDaemon detaches the sign-in daemon without showing a console
// window; the daemon records its own verified process identity for uninstall.
func startBackgroundDaemon(executable string) error {
	command := exec.Command(executable, "daemon")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start LCTK daemon: %w", err)
	}
	return command.Process.Release()
}
