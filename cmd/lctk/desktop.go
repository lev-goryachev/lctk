package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/lev-goryachev/lctk/internal/daemon"
)

var (
	desktopHealthURL = "http://" + daemon.DefaultAddress + "/health"
	desktopStart     = startBackgroundDaemon
	desktopOpen      = runAdminOpen
)

// runDesktop is the no-shell product entry point used by the Start-menu
// shortcut: reconnect to the sign-in daemon or start it, then open the one-time
// authenticated Admin UI.
func runDesktop(ctx context.Context, output io.Writer) error {
	if !daemonReady(ctx) {
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate LCTK executable: %w", err)
		}
		if err := desktopStart(executable); err != nil {
			return err
		}
		deadline := time.Now().Add(15 * time.Second)
		for !daemonReady(ctx) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if !daemonReady(ctx) {
			return errors.New("LCTK daemon did not become ready within 15 seconds")
		}
	}
	return desktopOpen(nil, output)
}

func daemonReady(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, desktopHealthURL, nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
