package desktopinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lev-goryachev/lctk/internal/releasebundle"
)

func TestInstallActivatesTheVerifiedLauncherBeforeRegistration(t *testing.T) {
	body := []byte("signed launcher")
	digest := sha256.Sum256(body)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(body) }))
	defer server.Close()
	home := t.TempDir()
	manager := NewManager(home)
	var registered string
	manager.Register = func(path, setup, version string) error {
		if installed, err := os.ReadFile(path); err != nil || string(installed) != string(body) {
			t.Fatalf("registration preceded verified activation: body=%q err=%v", installed, err)
		}
		registered = path
		if setup == "" || version != "1.0.0" {
			t.Fatalf("setup=%q version=%q", setup, version)
		}
		return nil
	}
	manifest := releasebundle.Manifest{Version: "1.0.0", Artifacts: []releasebundle.Artifact{{
		Name: "lctk.exe", Kind: "host-launcher", OS: "windows", Arch: "amd64",
		URL: server.URL, Bytes: int64(len(body)), SHA256: hex.EncodeToString(digest[:]),
	}, {
		Name: "setup.exe", Kind: "installer", OS: "windows", Arch: "amd64",
		URL: server.URL, Bytes: int64(len(body)), SHA256: hex.EncodeToString(digest[:]),
	}}}
	launcher, err := manager.Install(t.Context(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if launcher != filepath.Join(home, "bin", "lctk.exe") || registered != launcher {
		t.Fatalf("launcher=%q registered=%q", launcher, registered)
	}
}
