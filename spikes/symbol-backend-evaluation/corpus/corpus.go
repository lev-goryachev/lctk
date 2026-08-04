// Package corpus assembles the real source the candidates are measured against.
//
// Every language is represented by files from that language's own project rather
// than by a fixture. A fixture would be written by whoever writes the queries,
// which means it can only confirm that the queries match what their author
// expected. Real code contains the constructs nobody remembers to write down:
// generics, macros, conditional compilation, decorators, nested closures, and the
// generated files every project carries.
package corpus

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Source is one pinned upstream project.
type Source struct {
	// Language is the harness's name for what this project is measured as. A
	// project containing more than one language is listed once per language.
	Language string
	Name     string
	URL      string
	// Ref is a tag rather than a branch so a rerun reads the same bytes. The
	// resolved commit is reported with the results.
	Ref string
	// Extensions selects the files counted for this language.
	Extensions []string
}

// Sources are the pinned projects.
//
// They are small, widely read, and idiomatic for their language. ripgrep is here
// for a second reason: it is already this repository's declared correctness
// oracle for exact search, so the Rust corpus is code the project has committed
// to trusting elsewhere.
var Sources = []Source{
	{Language: "python", Name: "requests", URL: "https://github.com/psf/requests.git", Ref: "v2.32.3", Extensions: []string{".py"}},
	{Language: "rust", Name: "ripgrep", URL: "https://github.com/BurntSushi/ripgrep.git", Ref: "14.1.1", Extensions: []string{".rs"}},
	{Language: "c", Name: "zlib", URL: "https://github.com/madler/zlib.git", Ref: "v1.3.1", Extensions: []string{".c", ".h"}},
	{Language: "cpp", Name: "json", URL: "https://github.com/nlohmann/json.git", Ref: "v3.11.3", Extensions: []string{".cpp", ".hpp"}},
	{Language: "javascript", Name: "axios", URL: "https://github.com/axios/axios.git", Ref: "v1.7.7", Extensions: []string{".js"}},
	{Language: "typescript", Name: "axios", URL: "https://github.com/axios/axios.git", Ref: "v1.7.7", Extensions: []string{".ts"}},
}

// Fetch clones every distinct project into dir and returns the resolved commit
// for each, keyed by project name.
func Fetch(dir string) (map[string]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	resolved := map[string]string{}
	for _, source := range Sources {
		if _, done := resolved[source.Name]; done {
			continue
		}
		target := filepath.Join(dir, source.Name)
		if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
			clone := exec.Command("git", "clone", "--quiet", "--depth", "1",
				"--branch", source.Ref, source.URL, target)
			clone.Stderr = os.Stderr
			if err := clone.Run(); err != nil {
				return nil, fmt.Errorf("clone %s at %s: %w", source.Name, source.Ref, err)
			}
		}
		show := exec.Command("git", "-C", target, "rev-parse", "HEAD")
		out, err := show.Output()
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", source.Name, err)
		}
		resolved[source.Name] = strings.TrimSpace(string(out))
	}
	return resolved, nil
}

// File is one corpus member.
type File struct {
	Language string
	// Path is relative to the corpus directory, so a report does not carry the
	// machine's directory layout.
	Path string
	Full string
}

// Collect enumerates the corpus for one language, plus any extra roots given.
//
// A path containing a directory named `test`, `tests`, or `third_party` is kept.
// Excluding them would quietly measure the friendliest part of each project,
// and test code is where a language's more unusual syntax tends to live.
func Collect(dir string, extra map[string][]string) ([]File, error) {
	var files []File
	seen := map[string]struct{}{}

	add := func(language, root, full string) error {
		relative, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		key := language + "\x00" + full
		if _, already := seen[key]; already {
			return nil
		}
		seen[key] = struct{}{}
		files = append(files, File{Language: language, Path: filepath.ToSlash(relative), Full: full})
		return nil
	}

	walk := func(language, root string, extensions []string) error {
		return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if entry.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			for _, want := range extensions {
				if ext == want {
					return add(language, filepath.Dir(root), path)
				}
			}
			return nil
		})
	}

	for _, source := range Sources {
		root := filepath.Join(dir, source.Name)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		if err := walk(source.Language, root, source.Extensions); err != nil {
			return nil, err
		}
	}
	for language, roots := range extra {
		for _, root := range roots {
			extensions := ExtensionsFor(language)
			if len(extensions) == 0 {
				return nil, fmt.Errorf("no extensions known for %s", language)
			}
			if err := walk(language, root, extensions); err != nil {
				return nil, err
			}
		}
	}

	sort.Slice(files, func(a, b int) bool {
		if files[a].Language != files[b].Language {
			return files[a].Language < files[b].Language
		}
		return files[a].Path < files[b].Path
	})
	return files, nil
}

// extensionsByLanguage is the harness's own extension map.
var extensionsByLanguage = map[string][]string{
	"go":         {".go"},
	"python":     {".py"},
	"rust":       {".rs"},
	"c":          {".c", ".h"},
	"cpp":        {".cpp", ".hpp", ".cc", ".hh"},
	"javascript": {".js", ".mjs", ".cjs"},
	"typescript": {".ts"},
	"tsx":        {".tsx"},
}

// ExtensionsFor returns the extensions counted for a language.
func ExtensionsFor(language string) []string { return extensionsByLanguage[language] }
