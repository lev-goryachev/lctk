// lctk-launcher is the stable executable users invoke. Versioned host cores are
// immutable; update changes one verified activation document instead of
// replacing a running program in place.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/lev-goryachev/lctk/internal/installation"
	"github.com/lev-goryachev/lctk/internal/lctkhome"
)

// Official and dry-run workflows inject the final, post-signing core identity.
// A launcher without it fails closed instead of running an arbitrary sibling.
var (
	PackagedCoreSHA256 = ""
	PackagedCoreBytes  = ""
)

func main() {
	home, err := lctkhome.Dir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lctk: %v\n", err)
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lctk: locate launcher: %v\n", err)
		os.Exit(1)
	}
	executable, err := resolveExecutable(home, self)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lctk: %v\n", err)
		os.Exit(1)
	}
	command := exec.Command(executable, os.Args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "lctk: execute active host core: %v\n", err)
		os.Exit(1)
	}
}

func resolveExecutable(home, self string) (string, error) {
	executable, _, err := installation.ActiveExecutable(home)
	if err == nil {
		return executable, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// A freshly extracted signed archive has no host state yet. It may execute
	// only the sibling packaged core bound into this launcher at build time;
	// bootstrap then adopts that exact file.
	name := "lctk-core"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable = filepath.Join(filepath.Dir(self), name)
	size, parseErr := strconv.ParseInt(PackagedCoreBytes, 10, 64)
	if parseErr != nil || installation.VerifyExecutable(executable, size, PackagedCoreSHA256) != nil {
		return "", errors.New("no verified active or packaged host core is available")
	}
	return executable, nil
}
