package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/lev-goryachev/lctk/internal/adminsession"
	"github.com/lev-goryachev/lctk/internal/daemon"
)

// runAdminOpen prints, and by default opens, a signed link to the admin page.
//
// The code travels in the URL because that is the only channel a CLI has to a
// browser. It is spent on first use, so what survives in history is a code that
// no longer opens anything — which is the property that makes the URL acceptable
// as a carrier at all.
func runAdminOpen(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("admin open", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	address := flags.String("listen", daemon.DefaultAddress, "the daemon's loopback address")
	print := flags.Bool("print", false, "print the link instead of opening a browser")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: lctk admin open [--listen ADDRESS] [--print]")
	}

	code, err := adminsession.ReadCode("")
	if err != nil {
		return err
	}

	link := (&url.URL{
		Scheme:   "http",
		Host:     *address,
		Path:     "/admin/",
		RawQuery: url.Values{"code": []string{code}}.Encode(),
	}).String()

	if *print {
		fmt.Fprintln(stdout, link)
		return nil
	}
	if err := openBrowser(link); err != nil {
		fmt.Fprintf(stdout, "Open this link, which signs you in once:\n\n  %s\n", link)
		return nil
	}
	fmt.Fprintf(stdout, "Opened the admin page. The link signs you in once and is then spent.\n")
	return nil
}

// openBrowser asks the desktop to open a URL.
//
// Failure is not an error the caller has to handle: printing the link is a
// perfectly good outcome, and on a machine without a default browser it is the
// only one.
func openBrowser(link string) error {
	switch runtime.GOOS {
	case "windows":
		// rundll32 is used rather than "cmd /c start" because start treats the
		// ampersands in a query string as command separators.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", link).Start()
	case "darwin":
		return exec.Command("open", link).Start()
	default:
		return exec.Command("xdg-open", link).Start()
	}
}

func runAdmin(args []string, stdout, stderr io.Writer) error {
	const usage = "Usage:\n  lctk admin open [--listen ADDRESS] [--print]\n"
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("an admin subcommand is required")
	}
	switch args[0] {
	case "open":
		return runAdminOpen(args[1:], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown admin subcommand %q", strings.TrimSpace(args[0]))
	}
}
