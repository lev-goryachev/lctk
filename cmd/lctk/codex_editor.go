package main

// editorRunState is what LCTK could determine about an already-running editor.
//
// The distinction matters because a new window opened by a running editor is
// created by that process and inherits its environment, so the token LCTK would
// supply never reaches it. An honest "unknown" is more useful than a confident
// "no", since a wrong "no" turns into an integration failure the operator cannot
// explain.
type editorRunState string

const (
	editorRunningYes     editorRunState = "yes"
	editorRunningUnknown editorRunState = "unknown"
)
