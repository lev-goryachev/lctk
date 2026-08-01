//go:build !windows

package main

import "os/exec"

// editorRunning reports a running editor only when it can prove one.
//
// An exact-name match is evidence that the editor is running; the absence of one
// is not evidence that it is not, because editors on these platforms commonly
// run under a helper or framework process name unrelated to their launcher
// command. That case is reported as unknown so the caller is cautioned rather
// than reassured.
func editorRunning(editor string) editorRunState {
	if err := exec.Command("pgrep", "-x", editor).Run(); err == nil {
		return editorRunningYes
	}
	return editorRunningUnknown
}
