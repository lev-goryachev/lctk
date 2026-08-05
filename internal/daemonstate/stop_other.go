//go:build !windows

package daemonstate

import "errors"

func stop(string) error { return errors.New("managed daemon stop currently supports Windows only") }
