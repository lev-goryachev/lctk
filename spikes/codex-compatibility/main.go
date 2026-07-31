// Command codex-compat is the tracked Slice 0.4 evidence harness.
//
// It stands up an LCTK-shaped project-scoped Streamable HTTP MCP server, drives
// the real Codex CLI against it, and records what the actual client does. It is
// evidence for the Codex integration contract; it is not production code and
// does not become an LCTK dependency.
//
// The harness never reads or writes the operator's real Codex configuration. It
// always points Codex at an isolated CODEX_HOME inside the working directory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("a subcommand is required")
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "config":
		return runConfig(args[1:])
	case "verify":
		return runVerifyCommand(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `codex-compat — Slice 0.4 Codex compatibility evidence harness

Subcommands:
  serve    Run the project-scoped Streamable HTTP MCP server on its own.
  config   Print the CODEX_HOME config.toml that LCTK would have to generate.
  verify   Run the full scenario against the real Codex CLI and emit a report.
`)
}

func runServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:8123", "listen address")
	project := flags.String("project", primaryProject, "route-bound project identity")
	token := flags.String("token", "", "bearer token the route requires")
	stateless := flags.Bool("stateless", true, "serve stateless JSON responses")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		return errors.New("--token is required")
	}

	j := newJournal()
	j.tail = func(o observation) {
		encoded, _ := json.Marshal(o)
		fmt.Fprintln(os.Stderr, string(encoded))
	}
	projects := map[string]projectServer{
		*project: {ProjectID: *project, Token: *token, Sentinel: *project + "_only_sentinel"},
	}
	fmt.Fprintf(os.Stderr, "serving http://%s/projects/%s/mcp\n", *listen, *project)
	return http.ListenAndServe(*listen, newHandler(projects, j, *stateless))
}

func runConfig(args []string) error {
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	project := flags.String("project", primaryProject, "route-bound project identity")
	baseURL := flags.String("base-url", "http://127.0.0.1:8123", "harness base URL")
	tokenEnv := flags.String("token-env", tokenEnvPrimary, "environment variable holding the bearer token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	entry := codexServerEntry{
		Name:              *project,
		URL:               *baseURL + "/projects/" + *project + "/mcp",
		BearerTokenEnvVar: *tokenEnv,
		StartupTimeoutSec: 30,
		ToolTimeoutSec:    120,
		Enabled:           true,
		HTTPHeaders:       map[string]string{"X-Lctk-Project": *project},
	}
	fmt.Print(renderCodexHomeConfig([]codexServerEntry{entry}))
	return nil
}

func runVerifyCommand(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	codexFlag := flags.String("codex", "", "path to the Codex CLI (defaults to discovery)")
	workDir := flags.String("work", filepath.Join(".research", "codex-compat", "run"), "working directory for isolated state")
	out := flags.String("out", "", "write the JSON report to this path instead of stdout")
	keep := flags.Bool("keep", false, "keep the generated CODEX_HOME for inspection")
	if err := flags.Parse(args); err != nil {
		return err
	}

	absWork, err := filepath.Abs(*workDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absWork, 0o755); err != nil {
		return err
	}

	codexPath, discoverErr := discoverCodex(*codexFlag)
	if discoverErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; Codex-dependent steps will be skipped\n", discoverErr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	report, err := runVerify(ctx, codexPath, absWork, *keep)
	if err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if *out != "" {
		if err := os.WriteFile(*out, append(encoded, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "report written to %s\n", *out)
	} else {
		fmt.Println(string(encoded))
	}

	for _, s := range report.Steps {
		switch {
		case s.Skipped:
			fmt.Fprintf(os.Stderr, "SKIP %s: %s\n", s.Name, s.Detail)
		case s.Passed:
			fmt.Fprintf(os.Stderr, "PASS %s: %s\n", s.Name, s.Detail)
		default:
			fmt.Fprintf(os.Stderr, "FAIL %s: %s\n", s.Name, s.Detail)
		}
	}
	return nil
}
