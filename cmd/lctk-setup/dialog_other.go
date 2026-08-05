//go:build !windows

package main

import "fmt"

func showError(message string)       { fmt.Println("LCTK Setup:", message) }
func showInfo(message string)        { fmt.Println("LCTK Setup:", message) }
func confirmUninstall() (bool, bool) { return false, false }
