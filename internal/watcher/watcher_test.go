package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestObserveOnceSeesCreate(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := make(chan struct {
		change Change
		err    error
	}, 1)
	go func() {
		change, err := ObserveOnce(ctx, root)
		result <- struct {
			change Change
			err    error
		}{change: change, err: err}
	}()

	path := filepath.Join(root, "changed.txt")
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}

	observed := <-result
	if observed.err != nil {
		t.Fatal(observed.err)
	}
	if observed.change.Path != path {
		t.Fatalf("expected change for %q, got %#v", path, observed.change)
	}
}
