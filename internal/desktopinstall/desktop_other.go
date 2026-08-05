//go:build !windows

package desktopinstall

import "errors"

func registerDesktop(string, string, string) error {
	return errors.New("desktop installation currently supports Windows only")
}
func unregisterDesktop() error {
	return errors.New("desktop installation currently supports Windows only")
}
