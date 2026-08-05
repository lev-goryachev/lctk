package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNoArgumentDesktopEntryOpensTheAdminUI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	oldURL, oldStart, oldOpen := desktopHealthURL, desktopStart, desktopOpen
	desktopHealthURL = server.URL + "/health"
	desktopStart = func(string) error { t.Fatal("healthy daemon was restarted"); return nil }
	opened := false
	desktopOpen = func(args []string, _ io.Writer) error {
		opened = true
		if len(args) != 0 {
			t.Fatalf("args=%v", args)
		}
		return nil
	}
	t.Cleanup(func() { desktopHealthURL, desktopStart, desktopOpen = oldURL, oldStart, oldOpen })
	if err := run(t.Context(), nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("Admin UI was not opened")
	}
}
