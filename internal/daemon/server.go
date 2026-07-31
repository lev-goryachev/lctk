package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/lev-goryachev/lctk/internal/gateway"
	"github.com/lev-goryachev/lctk/internal/mcpserver"
)

const DefaultAddress = "127.0.0.1:4444"

type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// NewHandler builds the daemon's HTTP surface with production defaults.
func NewHandler() http.Handler {
	// The project-scoped endpoint from ADR-0001. Registry and grants are read per
	// request, so a project registered while the daemon runs becomes reachable
	// without a restart. Lifecycle gating is on, because a request served by a
	// stopped project would be answering about a stack that is not there.
	return NewHandlerWithGateway(gateway.Options{RequireRunning: true})
}

// NewHandlerWithGateway builds the daemon's HTTP surface with explicit gateway
// options. It exists so tests can supply an in-memory registry, grants, and
// status probe instead of requiring a container runtime.
func NewHandlerWithGateway(options gateway.Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(Health{
			Status:  "ok",
			Version: buildinfo.Version,
		})
	})
	mux.Handle("/mcp", mcpserver.NewHTTPHandler())
	gateway.New(options).Register(mux)
	return mux
}

func Run(ctx context.Context, address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}

	server := &http.Server{
		Handler:           NewHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve daemon API: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down daemon API: %w", err)
		}
		return nil
	}
}
