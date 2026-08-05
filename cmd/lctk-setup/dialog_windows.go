//go:build windows

package main

import "golang.org/x/sys/windows"

const (
	messageBoxYes = 6
	messageBoxNo  = 7
)

func showError(message string) {
	text, _ := windows.UTF16PtrFromString(message)
	title, _ := windows.UTF16PtrFromString("LCTK Setup")
	_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
}

func showInfo(message string) {
	text, _ := windows.UTF16PtrFromString(message)
	title, _ := windows.UTF16PtrFromString("LCTK Setup")
	_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONINFORMATION)
}

// confirmUninstall uses three explicit outcomes: Yes preserves project state
// archives, No removes all LCTK data, and Cancel changes nothing.
func confirmUninstall() (bool, bool) {
	message, _ := windows.UTF16PtrFromString("Remove LCTK and its managed runtime?\n\nYes: preserve project indexes and memory as archives.\nNo: remove all LCTK data.\nCancel: keep the installation.")
	title, _ := windows.UTF16PtrFromString("Uninstall LCTK")
	result, _ := windows.MessageBox(0, message, title, windows.MB_YESNOCANCEL|windows.MB_ICONWARNING)
	switch result {
	case messageBoxYes:
		return true, true
	case messageBoxNo:
		return false, true
	default:
		return false, false
	}
}
