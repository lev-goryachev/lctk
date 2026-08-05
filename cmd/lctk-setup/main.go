// lctk-setup is the signed browser-based Windows bootstrapper. It keeps the
// complete plan and mutation state in this process; the browser is only a local
// authenticated view over that state.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/releasebundle"
	"github.com/lev-goryachev/lctk/internal/setupflow"
	"github.com/lev-goryachev/lctk/internal/uninstall"
	"github.com/lev-goryachev/lctk/internal/windowssetup"
)

//go:embed page.html
var assets embed.FS

type state struct {
	Plan       setupflow.Plan `json:"plan"`
	Phase      string         `json:"phase"`
	Detail     string         `json:"detail"`
	Installing bool           `json:"installing"`
	Complete   bool           `json:"complete"`
	Reboot     bool           `json:"reboot_required"`
	Error      string         `json:"error,omitempty"`
}

type wizard struct {
	mu       sync.RWMutex
	state    state
	token    string
	manager  *setupflow.Manager
	manifest releasebundle.Manifest
	launcher string
	done     chan struct{}
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		showError(err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("lctk-setup", flag.ContinueOnError)
	manifestSource := flags.String("manifest", "", "signed release manifest HTTPS URL")
	resume := flags.Bool("resume", false, "resume after Windows restart")
	uninstallRequested := flags.Bool("uninstall", false, "remove LCTK and its managed runtime")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("setup does not accept positional arguments")
	}
	_ = resume
	if *uninstallRequested {
		preserve, proceed := confirmUninstall()
		if !proceed {
			return nil
		}
		home, err := lctkhome.Dir()
		if err != nil {
			return err
		}
		backup, err := uninstall.NewManager(home).Run(ctx, preserve)
		if err != nil {
			return err
		}
		message := "LCTK and its managed runtime were removed."
		if backup != "" {
			message += " Project state archives were preserved in " + backup + "."
		}
		showInfo(message)
		return nil
	}
	verifier, err := releasebundle.ProductionVerifier()
	if err != nil {
		return err
	}
	manifest, err := releasebundle.Load(ctx, *manifestSource, http.DefaultClient, verifier)
	if err != nil {
		return err
	}
	host, err := windowssetup.Probe(ctx)
	if err != nil {
		return err
	}
	if host.RequiresEnablement && !host.Elevated {
		arguments := []string{"--resume"}
		if *manifestSource != "" {
			arguments = append(arguments, "--manifest", *manifestSource)
		}
		return windowssetup.RelaunchElevated(arguments)
	}
	home, err := lctkhome.Dir()
	if err != nil {
		return err
	}
	manager := setupflow.NewManager(home, *manifestSource)
	plan, err := manager.Inspect(ctx, manifest)
	if err != nil {
		return err
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	w := &wizard{state: state{Plan: plan, Phase: "plan", Detail: "Review the verified installation plan."}, token: token, manager: manager, manifest: manifest, done: make(chan struct{})}
	manager.Progress = w.progress
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start setup interface: %w", err)
	}
	defer listener.Close()
	server := &http.Server{Handler: w.handler(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	link := "http://" + listener.Addr().String() + "/"
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", link).Start(); err != nil {
		return fmt.Errorf("open setup interface at %s: %w", link, err)
	}
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	case <-w.done:
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func (w *wizard) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", w.page)
	mux.HandleFunc("GET /api/status", w.status)
	mux.HandleFunc("POST /api/install", w.install)
	return mux
}

func (w *wizard) page(writer http.ResponseWriter, _ *http.Request) {
	body, err := assets.ReadFile("page.html")
	if err != nil {
		http.Error(writer, "setup interface is unavailable", http.StatusInternalServerError)
		return
	}
	page, err := template.New("setup").Parse(string(body))
	if err != nil {
		http.Error(writer, "setup interface is invalid", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
	_ = page.Execute(writer, map[string]string{"Token": w.token})
}

func (w *wizard) status(writer http.ResponseWriter, _ *http.Request) {
	w.mu.RLock()
	current := w.state
	w.mu.RUnlock()
	writeJSON(writer, http.StatusOK, current)
}

func (w *wizard) install(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("X-LCTK-Setup") != w.token {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "Invalid setup session."})
		return
	}
	w.mu.Lock()
	if w.state.Installing || w.state.Complete || w.state.Reboot {
		w.mu.Unlock()
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "Setup has already started."})
		return
	}
	w.state.Installing = true
	w.state.Phase = "starting"
	w.state.Detail = "Starting the accepted installation plan."
	w.mu.Unlock()
	go w.apply()
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "started"})
}

func (w *wizard) apply() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	err := w.manager.Install(ctx, w.manifest)
	w.mu.Lock()
	w.state.Installing = false
	switch {
	case errors.Is(err, setupflow.ErrRebootRequired):
		w.state.Reboot = true
		w.state.Phase = "reboot"
		w.state.Detail = err.Error()
	case err != nil:
		w.state.Error = err.Error()
		w.state.Phase = "failed"
		w.state.Detail = "Installation stopped without reporting success."
	default:
		w.state.Complete = true
		w.state.Phase = "complete"
		w.state.Detail = "LCTK is installed. Opening the interface."
	}
	w.mu.Unlock()
	if err == nil {
		launcher := w.launcher
		if launcher == "" {
			launcher = filepath.Join(w.manager.Home, "bin", "lctk.exe")
		}
		_ = exec.Command(launcher).Start()
		close(w.done)
	}
}

func (w *wizard) progress(phase, detail string) {
	w.mu.Lock()
	w.state.Phase, w.state.Detail = phase, detail
	w.mu.Unlock()
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create setup session: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
