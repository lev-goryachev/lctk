//go:build !windows

package windowssetup

import "context"

func Probe(context.Context) (Status, error)   { return Status{}, ErrUnsupportedHost }
func EnableWSL(context.Context) (bool, error) { return false, ErrUnsupportedHost }
func RelaunchElevated([]string) error         { return ErrUnsupportedHost }
func RegisterResume() error                   { return ErrUnsupportedHost }
