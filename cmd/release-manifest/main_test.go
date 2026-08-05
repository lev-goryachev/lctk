package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateKeyAndArtifactIdentity(t *testing.T) {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LCTK_RELEASE_ED25519_PRIVATE_KEY", base64.StdEncoding.EncodeToString(private.Seed()))
	loaded, err := privateKeyFromEnvironment()
	if err != nil || !loaded.Public().(ed25519.PublicKey).Equal(private.Public()) {
		t.Fatalf("privateKeyFromEnvironment: %v", err)
	}

	path := filepath.Join(t.TempDir(), "artifact.bin")
	if err := os.WriteFile(path, []byte("release bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts, err := loadArtifacts([]string{"artifact.bin,host-core,windows,amd64," + path}, "https://example.invalid/release")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Bytes != int64(len("release bytes")) || len(artifacts[0].SHA256) != 64 {
		t.Fatalf("artifact identity = %+v", artifacts)
	}
}

func TestImageDigestRejectsMutableReference(t *testing.T) {
	if _, err := imageDigest("registry.example/code:latest"); err == nil {
		t.Fatal("imageDigest accepted a mutable tag")
	}
	digest := strings.Repeat("a", 64)
	got, err := imageDigest("registry.example/code@sha256:" + digest)
	if err != nil || got != "sha256:"+digest {
		t.Fatalf("imageDigest = %q, %v", got, err)
	}
}
