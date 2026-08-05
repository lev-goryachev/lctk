package releasebundle

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAuthenticatesLocalEnvelope(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(Envelope{
		KeyID: "test", Algorithm: "ed25519",
		Payload:   base64.StdEncoding.EncodeToString(payload),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release.json")
	if err := os.WriteFile(path, envelope, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := Load(context.Background(), path, nil, Verifier{KeyID: "test", PublicKey: public})
	if err != nil || manifest.Version != "1.0.0" {
		t.Fatalf("Load = %+v, %v", manifest, err)
	}
}

func TestLoadRejectsInsecureRemoteSource(t *testing.T) {
	if _, err := Load(context.Background(), "http://example.invalid/release.json", nil,
		Verifier{KeyID: "test", PublicKey: make(ed25519.PublicKey, ed25519.PublicKeySize)}); err == nil {
		t.Fatal("Load accepted an insecure remote source")
	}
}
