// Command code-intel is the per-project code-intelligence service.
//
// One instance runs in each project's container, with that project's source
// mounted read-only at /workspace and its persistent state in a volume at
// /var/lib/lctk. It serves exact search over the project's own network only; the
// host gateway is the intended caller and enforces route-bound scope before any
// request reaches here.
//
// The process holds no project identifier it could be persuaded to change. It
// can search exactly one workspace because exactly one is mounted.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/httpapi"
	"github.com/lev-goryachev/lctk/images/code-intel/internal/searchindex"
	"github.com/lev-goryachev/lctk/images/code-intel/internal/symbols"
)

// ListenPort is fixed inside the container. The host side is published on an
// ephemeral loopback port, so nothing here needs to be configurable to avoid a
// collision.
const ListenPort = 8080

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("code-intel exited", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	workspace := envOr("LCTK_WORKSPACE", "/workspace")
	stateDir := envOr("LCTK_STATE_DIR", "/var/lib/lctk")
	projectID := envOr("LCTK_PROJECT_ID", "unknown")

	if _, err := os.Stat(workspace); err != nil {
		return err
	}
	indexRoot := filepath.Join(stateDir, "index")
	if err := os.MkdirAll(indexRoot, 0o755); err != nil {
		return err
	}

	store := searchindex.New(workspace, indexRoot, projectID, limitsFromEnv())
	defer store.Close()

	// The symbol engine is built before the listener, because a query that does not
	// compile against the grammar loaded here is a configuration error and must not
	// become an empty answer discovered later on one file.
	outliner, err := symbols.New()
	if err != nil {
		return err
	}
	defer outliner.Close()
	applySymbolLimits(outliner)

	server := httpapi.New(store, outliner, logger)
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(ListenPort))
	if err != nil {
		return err
	}

	// Readiness is published once the listener is accepting, not once the index
	// is built. A large project can take a while to index, and reporting the
	// container unhealthy for that whole time would be wrong: it is up, and it
	// answers a search with a typed retryable "not ready yet".
	readyMarker := filepath.Join(stateDir, "ready")
	if err := os.WriteFile(readyMarker, []byte("ok\n"), 0o644); err != nil {
		return err
	}
	defer os.Remove(readyMarker)

	logger.Info("code-intel listening",
		slog.String("project_id", projectID),
		slog.String("workspace", workspace),
		slog.String("index_root", indexRoot),
		slog.Int("port", ListenPort),
		slog.Any("outline_languages", outliner.Languages()))

	// The first index build runs alongside the listener rather than before it, so
	// the host can observe status and receive typed errors while it happens.
	go func() {
		if err := server.EnsureIndexed(ctx); err != nil {
			logger.Error("initial index build failed", slog.String("error", err.Error()))
		}
	}()

	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// applySymbolLimits lets an operator relax or tighten the parse bounds for a
// project with unusual content, in the same style as the index limits.
func applySymbolLimits(engine *symbols.Engine) {
	if value, err := time.ParseDuration(os.Getenv("LCTK_SYMBOL_BUDGET")); err == nil && value > 0 {
		engine.Budget = value
	}
	if value, err := strconv.ParseInt(os.Getenv("LCTK_SYMBOL_MAX_FILE_BYTES"), 10, 64); err == nil && value > 0 {
		engine.MaxFileBytes = value
	}
	// The parse bound comes from the host's background-load policy, the same figure
	// that caps index work, because it answers the same question: how much of this
	// machine the project may spend. A container with no policy still gets a bound --
	// unbounded parsing is what Slice 4.5 measured turning a busy service into one
	// that refuses ordinary files.
	engine.SetParallelism(symbolParallelism())
}

// symbolParallelism resolves how many files may be parsed at once.
func symbolParallelism() int {
	if value, err := strconv.Atoi(os.Getenv("LCTK_SYMBOL_PARALLELISM")); err == nil && value > 0 {
		return value
	}
	if value, err := strconv.Atoi(os.Getenv("LCTK_INDEX_PARALLELISM")); err == nil && value > 0 {
		return value
	}
	// No policy was delivered. runtime.NumCPU inside a CPU-limited container reports
	// the host's processors rather than the share this project may use, so it is a
	// ceiling rather than an answer -- but a ceiling is what is missing here.
	return runtime.NumCPU()
}

// limitsFromEnv allows the shipped policy to be overridden for a project with
// unusual content, without making the defaults a per-project decision.
func limitsFromEnv() searchindex.Limits {
	limits := searchindex.DefaultLimits
	if value, err := strconv.ParseInt(os.Getenv("LCTK_INDEX_MAX_FILE_BYTES"), 10, 64); err == nil && value > 0 {
		limits.MaxFileBytes = value
	}
	if value, err := strconv.Atoi(os.Getenv("LCTK_INDEX_MAX_DELTAS")); err == nil && value > 0 {
		limits.MaxDeltaGenerations = value
	}
	// The parallelism cap comes from the host's background-load policy. The
	// container cannot see that policy and would otherwise size itself to
	// whatever the runtime lets it see, which on a CPU-limited container is the
	// whole machine.
	if value, err := strconv.Atoi(os.Getenv("LCTK_INDEX_PARALLELISM")); err == nil && value > 0 {
		limits.Parallelism = value
	}
	return limits
}
