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
	"github.com/lev-goryachev/lctk/internal/watchsupervisor"
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

// changeReporter adapts the watch supervisor to the gateway's vocabulary, so the
// gateway does not need to know that a supervisor exists.
func changeReporter(supervisor *watchsupervisor.Supervisor) gateway.ChangeReporter {
	return func(projectID string) (gateway.ChangeState, bool) {
		view, ok := supervisor.View(projectID)
		if !ok {
			return gateway.ChangeState{}, false
		}
		state := gateway.ChangeState{
			Watching:        view.Watching,
			Pending:         view.Pending,
			LastEventAt:     view.LastEventAt,
			DebounceSeconds: view.DebounceSeconds,
		}
		if view.Gap != nil {
			state.GapReason = view.Gap.Reason
		}
		return state, true
	}
}

func Run(ctx context.Context, address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}

	// The watcher lives in the daemon rather than in the CLI because it must
	// outlive any one command, and on the host rather than in the container
	// because that is where the native events are reliable.
	supervisor := watchsupervisor.New(watchsupervisor.Options{})
	go supervisor.Run(ctx)

	server := &http.Server{
		Handler: NewHandlerWithGateway(gateway.Options{
			RequireRunning: true,
			Wake:           supervisor.Wake,
			Changes:        changeReporter(supervisor),
			Flush:          supervisor.Flush,
		}),
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
