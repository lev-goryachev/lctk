package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestResolveExecutableVerifiesPackagedCore(t *testing.T) {
	directory := t.TempDir()
	name := "lctk-core"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	core := filepath.Join(directory, name)
	content := []byte("signed packaged core")
	if err := os.WriteFile(core, content, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	oldDigest, oldBytes := PackagedCoreSHA256, PackagedCoreBytes
	PackagedCoreSHA256 = hex.EncodeToString(digest[:])
	PackagedCoreBytes = strconv.Itoa(len(content))
	t.Cleanup(func() {
		PackagedCoreSHA256, PackagedCoreBytes = oldDigest, oldBytes
	})

	resolved, err := resolveExecutable(filepath.Join(directory, "empty-home"), filepath.Join(directory, "lctk"))
	if err != nil || resolved != core {
		t.Fatalf("resolveExecutable = %q, %v; want %q", resolved, err, core)
	}
	if err := os.WriteFile(core, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExecutable(filepath.Join(directory, "empty-home"), filepath.Join(directory, "lctk")); err == nil {
		t.Fatal("resolveExecutable accepted a tampered packaged core")
	}
}
