package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/codexconfig"
	"github.com/lev-goryachev/lctk/internal/projectgrant"
)

func codex(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runCodex(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// isolateCodexHome points the Codex commands at a temporary directory, so a test
// never reads or writes the developer's real Codex configuration.
func isolateCodexHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(codexconfig.EnvHome, home)
	return home
}

func codexConfigContents(t *testing.T) string {
	t.Helper()
	path, err := codexconfig.Path()
	if err != nil {
		t.Fatal(err)
	}
	document, err := codexconfig.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func tokenFor(t *testing.T, projectID string) string {
	t.Helper()
	grants, err := projectgrant.Load()
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.ForProject(projectID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return grant.Token
}

func TestCodexConfigPreviewsBeforeWriting(t *testing.T) {
	isolateHome(t)
	isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	stdout, _, err := codex(t, "config", id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, codexconfig.EntryName(id)) {
		t.Errorf("the preview does not show the entry:\n%s", stdout)
	}
	if !strings.Contains(stdout, "--apply") {
		t.Errorf("the preview does not say how to write it:\n%s", stdout)
	}
	if got := codexConfigContents(t); got != "" {
		t.Errorf("a preview wrote to the configuration file:\n%s", got)
	}
}

func TestCodexConfigApplyWritesAnEntryWithoutTheToken(t *testing.T) {
	isolateHome(t)
	isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	if _, _, err := codex(t, "config", "--apply", id); err != nil {
		t.Fatal(err)
	}

	document := codexConfigContents(t)
	if !strings.Contains(document, "[mcp_servers."+codexconfig.EntryName(id)+"]") {
		t.Errorf("the entry was not written:\n%s", document)
	}
	if !strings.Contains(document, projectgrant.EnvVarName(id)) {
		t.Errorf("the entry does not reference the credential variable:\n%s", document)
	}
	// The measured Codex contract forbids an inline credential, and ADR-0014
	// makes "no generated file holds a secret" a property to keep.
	if token := tokenFor(t, id); strings.Contains(document, token) {
		t.Error("the grant token was written into the Codex configuration")
	}
}

func TestCodexConfigApplyIsIdempotentAndBacksUp(t *testing.T) {
	isolateHome(t)
	home := isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	if _, _, err := codex(t, "config", "--apply", id); err != nil {
		t.Fatal(err)
	}
	first := codexConfigContents(t)

	stdout, _, err := codex(t, "config", "--apply", "--json", id)
	if err != nil {
		t.Fatal(err)
	}
	var view codexConfigView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.Changed || view.Applied {
		t.Errorf("re-applying an unchanged entry rewrote the file: %+v", view)
	}
	if second := codexConfigContents(t); second != first {
		t.Error("re-applying changed the document")
	}

	// Writing over an existing file leaves a recoverable copy, because LCTK is
	// not the only writer of this file.
	seeded := filepath.Join(home, codexconfig.FileName)
	if err := os.WriteFile(seeded, []byte("model = \"gpt-5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := codex(t, "config", "--apply", id); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(seeded + codexconfig.BackupSuffix)
	if err != nil {
		t.Fatalf("no backup was taken: %v", err)
	}
	if string(backup) != "model = \"gpt-5\"\n" {
		t.Errorf("backup = %q", backup)
	}
	if !strings.Contains(codexConfigContents(t), "model = \"gpt-5\"") {
		t.Error("writing the entry discarded unrelated configuration")
	}
}

func TestCodexConfigRefusesAForeignEntryUntilForced(t *testing.T) {
	isolateHome(t)
	home := isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	foreign := "[mcp_servers." + codexconfig.EntryName(id) + "]\nurl = \"http://elsewhere/mcp\"\n"
	if err := os.WriteFile(filepath.Join(home, codexconfig.FileName), []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := codex(t, "config", "--apply", id)
	if err == nil {
		t.Fatal("LCTK overwrote an entry it did not generate")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal does not say how to proceed: %v", err)
	}
	if codexConfigContents(t) != foreign {
		t.Error("the refused write still changed the file")
	}

	if _, _, err := codex(t, "config", "--apply", "--force", id); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(codexConfigContents(t), "elsewhere") {
		t.Error("--force did not replace the foreign entry")
	}
}

func TestCodexConfigRemoveDropsOnlyTheLctkEntry(t *testing.T) {
	isolateHome(t)
	home := isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	if err := os.WriteFile(filepath.Join(home, codexconfig.FileName),
		[]byte("[mcp_servers.other]\ncommand = \"node\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := codex(t, "config", "--apply", id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := codex(t, "config", "--apply", "--remove", id); err != nil {
		t.Fatal(err)
	}

	document := codexConfigContents(t)
	if strings.Contains(document, codexconfig.EntryName(id)) {
		t.Errorf("the entry survived removal:\n%s", document)
	}
	if !strings.Contains(document, "[mcp_servers.other]") {
		t.Errorf("removal took an unrelated server with it:\n%s", document)
	}
}

func TestCodexStatusReportsPlacementAndCredentialVariable(t *testing.T) {
	isolateHome(t)
	isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	stdout, _, err := codex(t, "status", "--json", id)
	if err != nil {
		t.Fatal(err)
	}
	var before codexStatusView
	if err := json.Unmarshal([]byte(stdout), &before); err != nil {
		t.Fatal(err)
	}
	if len(before.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(before.Projects))
	}
	if before.Projects[0].Placement != string(codexconfig.PlacementAbsent) {
		t.Errorf("placement = %q, want absent", before.Projects[0].Placement)
	}
	if !before.Projects[0].GrantIssued {
		t.Error("registering a project did not report an issued grant")
	}
	if before.Projects[0].TokenEnvVar != projectgrant.EnvVarName(id) {
		t.Errorf("token_env_var = %q", before.Projects[0].TokenEnvVar)
	}

	if _, _, err := codex(t, "config", "--apply", id); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = codex(t, "status", "--json", id)
	if err != nil {
		t.Fatal(err)
	}
	var after codexStatusView
	if err := json.Unmarshal([]byte(stdout), &after); err != nil {
		t.Fatal(err)
	}
	if after.Projects[0].Placement != string(codexconfig.PlacementManaged) {
		t.Errorf("placement = %q, want managed", after.Projects[0].Placement)
	}

	if strings.Contains(stdout, tokenFor(t, id)) {
		t.Error("status printed the token")
	}
}

func TestCodexEnvWithholdsTheTokenAndSetsNothing(t *testing.T) {
	isolateHome(t)
	isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")
	token := tokenFor(t, id)
	variable := projectgrant.EnvVarName(id)

	stdout, _, err := codex(t, "env", id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, token) {
		t.Error("the token was printed without --reveal")
	}
	if _, present := os.LookupEnv(variable); present {
		// ADR-0014 keeps a durable change to the machine an act the operator
		// performs, so no LCTK command may set the variable itself.
		t.Error("running the command set the credential variable")
	}

	stdout, _, err = codex(t, "env", "--reveal", id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, token) {
		t.Error("--reveal did not print the token")
	}
	if _, present := os.LookupEnv(variable); present {
		t.Error("--reveal set the credential variable")
	}
}

// fakeEditor installs an executable on PATH that records the environment it was
// started with, so a test can prove the credential actually reaches the child
// process rather than only being reported as intended.
func fakeEditor(t *testing.T) (name, outputPath string) {
	t.Helper()
	dir := t.TempDir()
	outputPath = filepath.Join(dir, "environment.txt")
	t.Setenv("LCTK_FAKE_EDITOR_OUT", outputPath)

	// The recording is written to a temporary name and moved into place, so a
	// reader that finds the file always finds it complete. Polling for a
	// non-empty file would otherwise race a partially written one.
	name = "lctkfakeeditor"
	var script string
	var path string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, name+".bat")
		script = "@echo off\r\n" +
			"set > \"%LCTK_FAKE_EDITOR_OUT%.tmp\"\r\n" +
			"move /y \"%LCTK_FAKE_EDITOR_OUT%.tmp\" \"%LCTK_FAKE_EDITOR_OUT%\" >nul\r\n"
	} else {
		path = filepath.Join(dir, name)
		script = "#!/bin/sh\n" +
			"env > \"$LCTK_FAKE_EDITOR_OUT.tmp\"\n" +
			"mv \"$LCTK_FAKE_EDITOR_OUT.tmp\" \"$LCTK_FAKE_EDITOR_OUT\"\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return name, outputPath
}

func TestCodexLaunchDeliversTheTokenToTheChildEnvironment(t *testing.T) {
	isolateHome(t)
	isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")
	token := tokenFor(t, id)
	editor, output := fakeEditor(t)

	stdout, _, err := codex(t, "launch", "--editor", editor, "--json", id)
	if err != nil {
		t.Fatal(err)
	}
	var view codexLaunchView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Started {
		t.Fatal("the editor was not started")
	}
	if strings.Contains(stdout, token) {
		t.Error("launch printed the token")
	}

	deadline := time.Now().Add(20 * time.Second)
	var recorded []byte
	for {
		recorded, err = os.ReadFile(output)
		if err == nil && len(recorded) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the editor never recorded its environment: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !strings.Contains(string(recorded), projectgrant.EnvVarName(id)) {
		t.Errorf("the child process did not receive the credential variable; it recorded %d bytes:\n%s",
			len(recorded), lctkLines(string(recorded)))
	} else if !strings.Contains(string(recorded), token) {
		t.Error("the child process received the variable but not the token value")
	}
	// Delivery must be scoped to the started process and leave nothing behind.
	if _, present := os.LookupEnv(projectgrant.EnvVarName(id)); present {
		t.Error("launching leaked the credential into LCTK's own environment")
	}
}

func TestCodexLaunchDryRunStartsNothing(t *testing.T) {
	isolateHome(t)
	isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")
	editor, output := fakeEditor(t)

	stdout, _, err := codex(t, "launch", "--editor", editor, "--dry-run", id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, projectgrant.EnvVarName(id)) {
		t.Errorf("the dry run does not name the variable it would set:\n%s", stdout)
	}
	if _, err := os.Stat(output); err == nil {
		t.Error("the dry run started the editor")
	}
}

func TestCodexLaunchReportsAMissingEditor(t *testing.T) {
	isolateHome(t)
	isolateCodexHome(t)
	healthyRuntime(t)
	id := addProject(t, "alpha")

	_, _, err := codex(t, "launch", "--editor", "lctk-no-such-editor", id)
	if err == nil {
		t.Fatal("a missing editor was not reported")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("the error does not explain the failure: %v", err)
	}
}

// lctkLines reduces a recorded environment to the LCTK entries, so a failure
// reports what the child actually received without printing the whole
// environment or the token value of an unrelated variable.
func lctkLines(recorded string) string {
	var kept []string
	for _, line := range strings.Split(strings.ReplaceAll(recorded, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.ToUpper(line), "LCTK") {
			name, _, _ := strings.Cut(line, "=")
			kept = append(kept, name)
		}
	}
	if len(kept) == 0 {
		return "(no LCTK variables recorded)"
	}
	return strings.Join(kept, "\n")
}
