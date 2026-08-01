// Command codex-end-to-end runs the Slice 1.4 scenario against real components:
// the real LCTK executable, the real container runtime, the real project
// endpoint, and the Codex binary the VS Code extension itself runs.
//
// Nothing here is simulated. When a component is unavailable the affected steps
// are reported as skipped, never as passed. Everything the run creates lives
// under a work directory with its own LCTK home and its own CODEX_HOME, so the
// operator's real state is neither read nor modified.
//
// This harness verifies the client boundary that can be driven without a human.
// It does not click the extension's user interface; that part of the Slice 1.4
// scenario is recorded separately in the results document.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const usage = `Slice 1.4 Codex end-to-end harness

Usage:
  go run ./spikes/codex-end-to-end verify [flags]

Flags:
  --codex PATH   Codex binary; defaults to the one bundled with the VS Code extension
  --lctk PATH    lctk executable; built into the work directory when omitted
  --work DIR     work directory; a temporary directory when omitted
  --keep         keep the work directory, the containers, and the registrations
  --json         write the evidence report as JSON
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Print(usage)
		return
	}
	if os.Args[1] != "verify" {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	flags := flag.NewFlagSet("verify", flag.ExitOnError)
	codexPath := flags.String("codex", "", "path to the Codex binary")
	lctkPath := flags.String("lctk", "", "path to the lctk executable")
	workDir := flags.String("work", "", "work directory")
	keep := flags.Bool("keep", false, "keep everything the run creates")
	asJSON := flags.Bool("json", false, "write the report as JSON")
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}

	if err := verify(ctx, *codexPath, *lctkPath, *workDir, *keep, *asJSON); err != nil {
		fmt.Fprintf(os.Stderr, "codex-end-to-end: %v\n", err)
		os.Exit(1)
	}
}

func verify(ctx context.Context, codexPath, lctkPath, workDir string, keep, asJSON bool) error {
	work, cleanup, err := prepareWorkDir(workDir, keep)
	if err != nil {
		return err
	}
	defer cleanup()

	codex, err := discoverCodex(codexPath)
	if err != nil {
		return err
	}
	lctk, err := buildLctk(ctx, lctkPath, work)
	if err != nil {
		return err
	}

	lctkHome := filepath.Join(work, "lctk-home")
	codexHome := filepath.Join(work, "codex-home")
	for _, dir := range []string{lctkHome, codexHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	env := &environment{
		lctk:      lctk,
		codex:     codex,
		work:      work,
		lctkHome:  lctkHome,
		codexHome: codexHome,
		env: append(os.Environ(),
			"LCTK_HOME="+lctkHome,
			"CODEX_HOME="+codexHome,
		),
	}

	report, err := runChain(ctx, env, keep)
	if err != nil {
		return err
	}

	if asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		printReport(report)
	}
	if report.failed() > 0 {
		return fmt.Errorf("%d step(s) failed", report.failed())
	}
	return nil
}

func printReport(r *report) {
	fmt.Printf("platform:  %s\n", r.Platform)
	fmt.Printf("lctk:      %s\n", r.LctkVersion)
	fmt.Printf("codex:     %s (%s)\n", r.CodexVersion, r.CodexPath)
	fmt.Printf("docker:    %s\n", r.DockerVersion)
	fmt.Printf("endpoint:  %s\n\n", r.Endpoint)
	for _, step := range r.Steps {
		mark := "FAIL"
		switch {
		case step.Skipped:
			mark = "SKIP"
		case step.Passed:
			mark = "PASS"
		}
		fmt.Printf("%s  %-36s %s\n", mark, step.Name, step.Detail)
	}
	fmt.Printf("\n%d failed\n", r.failed())
}

func prepareWorkDir(workDir string, keep bool) (string, func(), error) {
	if workDir != "" {
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return "", nil, err
		}
		return workDir, func() {}, nil
	}
	temp, err := os.MkdirTemp("", "lctk-codex-e2e-")
	if err != nil {
		return "", nil, err
	}
	if keep {
		return temp, func() { fmt.Printf("\nwork directory kept at %s\n", temp) }, nil
	}
	return temp, func() { _ = os.RemoveAll(temp) }, nil
}

// buildLctk compiles the executable under test, so the harness exercises the
// commands an operator runs rather than an internal shortcut.
func buildLctk(ctx context.Context, explicit, work string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("lctk executable %q: %w", explicit, err)
		}
		return explicit, nil
	}
	name := "lctk"
	if os.PathSeparator == '\\' {
		name = "lctk.exe"
	}
	output := filepath.Join(work, name)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", output, "./cmd/lctk")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build lctk: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return output, nil
}

// discoverCodex resolves the Codex binary, preferring the one bundled with the
// VS Code extension because that is the binary the extension actually runs.
func discoverCodex(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("codex binary %q: %w", explicit, err)
		}
		return explicit, nil
	}
	if env := os.Getenv("LCTK_CODEX_BIN"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		// The extension ships binaries for several platforms side by side, so the
		// base name alone is not enough: on Windows the Linux build is also called
		// "codex" and sorts first.
		wanted := "codex"
		if runtime.GOOS == "windows" {
			wanted = "codex.exe"
		}
		pattern := filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-*", "bin", "*", wanted)
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			return matches[len(matches)-1], nil
		}
	}
	if found, err := exec.LookPath("codex"); err == nil {
		return found, nil
	}
	return "", errors.New("no Codex binary found; pass --codex or set LCTK_CODEX_BIN")
}
