package verifieddownload

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lev-goryachev/lctk/internal/releasebundle"
)

func TestDownloadActivatesOnlyTheExactSignedIdentity(t *testing.T) {
	body := []byte("verified bytes")
	digest := sha256.Sum256(body)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(body) }))
	defer server.Close()
	artifact := releasebundle.Artifact{Name: "component", URL: server.URL, Bytes: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}
	target := filepath.Join(t.TempDir(), "nested", "component")
	if err := Download(t.Context(), http.DefaultClient, artifact, target); err != nil {
		t.Fatal(err)
	}
	if err := Verify(target, artifact.Bytes, artifact.SHA256); err != nil {
		t.Fatal(err)
	}
	bad := artifact
	bad.SHA256 = strings.Repeat("0", 64)
	badTarget := filepath.Join(t.TempDir(), "bad")
	if err := Download(t.Context(), http.DefaultClient, bad, badTarget); err == nil {
		t.Fatal("wrong digest was activated")
	}
	if _, err := os.Stat(badTarget); !os.IsNotExist(err) {
		t.Fatalf("bad target exists: %v", err)
	}
}

func TestDownloadReplacesARejectedExistingArtifact(t *testing.T) {
	body := []byte("verified replacement")
	digest := sha256.Sum256(body)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(body)
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "artifact.bin")
	if err := os.WriteFile(target, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := releasebundle.Artifact{Name: "artifact.bin", URL: server.URL, Bytes: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}
	if err := Download(t.Context(), server.Client(), artifact, target); err != nil {
		t.Fatal(err)
	}
	if err := Verify(target, artifact.Bytes, artifact.SHA256); err != nil {
		t.Fatalf("replacement identity: %v", err)
	}
}
