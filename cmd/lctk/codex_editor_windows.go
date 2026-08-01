package main

import (
	"os/exec"
	"strings"
)

// editorRunning asks the task list whether an image matching the editor command
// is already running. The filter is case-insensitive, so the command name is
// used directly.
func editorRunning(editor string) editorRunState {
	image := strings.TrimSuffix(strings.TrimSuffix(editor, ".cmd"), ".exe") + ".exe"
	output, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+image, "/NH").Output()
	if err != nil {
		return editorRunningUnknown
	}
	if strings.Contains(strings.ToLower(string(output)), strings.ToLower(image)) {
		return editorRunningYes
	}
	// tasklist reports no match on standard output rather than through an exit
	// code, so an absent image name is a genuine negative. It is still reported
	// as unknown, because the editor may run under an image name that does not
	// match its launcher command.
	return editorRunningUnknown
}
