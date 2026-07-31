package main

import (
	"testing"

	"github.com/lev-goryachev/lctk/spikes/search-backend-evaluation/zoektadapter"
)

func TestNormalizeRelativePath(t *testing.T) {
	for input, want := range map[string]string{
		"./src/router.ts":  "src/router.ts",
		"src/router.ts":    "src/router.ts",
		"src/../README.md": "README.md",
	} {
		if got := normalizeRelativePath(input); got != want {
			t.Errorf("normalizeRelativePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSummarizeManifest(t *testing.T) {
	got := summarizeManifest(zoektadapter.Manifest{
		Generation: 7,
		Files: map[string]string{
			"src/a.go": "a",
			"src/b.go": "b",
		},
	})
	if got.Generation != 7 || got.Files != 2 {
		t.Fatalf("summary = %+v, want generation 7 and two files", got)
	}
}
