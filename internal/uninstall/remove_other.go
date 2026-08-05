//go:build !windows

package uninstall

import "errors"

func scheduleRemoval(string) error { return errors.New("uninstall currently supports Windows only") }
