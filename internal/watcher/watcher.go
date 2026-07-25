package watcher

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

type Change struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
}

func ObserveOnce(ctx context.Context, root string) (Change, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Change{}, fmt.Errorf("resolve watch root: %w", err)
	}

	watch, err := fsnotify.NewWatcher()
	if err != nil {
		return Change{}, fmt.Errorf("create filesystem watcher: %w", err)
	}
	defer watch.Close()
	if err := watch.Add(absoluteRoot); err != nil {
		return Change{}, fmt.Errorf("watch %s: %w", absoluteRoot, err)
	}

	select {
	case event := <-watch.Events:
		return Change{Path: event.Name, Operation: event.Op.String()}, nil
	case err := <-watch.Errors:
		return Change{}, fmt.Errorf("watch %s: %w", absoluteRoot, err)
	case <-ctx.Done():
		return Change{}, fmt.Errorf("wait for filesystem change: %w", ctx.Err())
	}
}
