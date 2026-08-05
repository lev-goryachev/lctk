//go:build windows

package desktopinstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

func registerDesktop(launcher, uninstaller, version string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open sign-in startup registry: %w", err)
	}
	if err := key.SetStringValue("LCTK", `"`+launcher+`" daemon`); err != nil {
		key.Close()
		return fmt.Errorf("register sign-in daemon: %w", err)
	}
	if err := key.Close(); err != nil {
		return fmt.Errorf("close sign-in startup registry: %w", err)
	}
	programs, err := windows.KnownFolderPath(windows.FOLDERID_StartMenuAllPrograms, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return fmt.Errorf("locate Start menu: %w", err)
	}
	dir := filepath.Join(programs, "LCTK")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Start-menu group: %w", err)
	}
	shortcut := filepath.Join(dir, "LCTK.lnk")
	script := `$shell = New-Object -ComObject WScript.Shell; $shortcut = $shell.CreateShortcut($args[1]); $shortcut.TargetPath = $args[0]; $shortcut.WorkingDirectory = Split-Path $args[0]; $shortcut.Description = 'Open LCTK'; $shortcut.Save()`
	if output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script, launcher, shortcut).CombinedOutput(); err != nil {
		return fmt.Errorf("create Start-menu shortcut: %s: %w", string(output), err)
	}
	uninstall, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\LCTK`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open uninstall registry: %w", err)
	}
	defer uninstall.Close()
	for name, value := range map[string]string{
		"DisplayName":     "LCTK",
		"DisplayVersion":  version,
		"Publisher":       "LCTK contributors",
		"UninstallString": `"` + uninstaller + `" --uninstall`,
	} {
		if err := uninstall.SetStringValue(name, value); err != nil {
			return fmt.Errorf("register uninstaller: %w", err)
		}
	}
	return nil
}

func unregisterDesktop() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err == nil {
		if deleteErr := key.DeleteValue("LCTK"); deleteErr != nil && deleteErr != registry.ErrNotExist {
			key.Close()
			return fmt.Errorf("remove sign-in daemon registration: %w", deleteErr)
		}
		_ = key.Close()
	}
	programs, err := windows.KnownFolderPath(windows.FOLDERID_StartMenuAllPrograms, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return fmt.Errorf("locate Start menu: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(programs, "LCTK")); err != nil {
		return fmt.Errorf("remove Start-menu group: %w", err)
	}
	if err := registry.DeleteKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\LCTK`); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("remove uninstall registration: %w", err)
	}
	return nil
}
