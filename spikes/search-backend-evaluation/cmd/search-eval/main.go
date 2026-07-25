// Command search-eval is a reproducible evidence harness for the Slice 0.3
// search-backend evaluation. It is not a production LCTK command.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/spikes/search-backend-evaluation/zoektadapter"
)

const zoektRevision = "2b2ce2e398e6bee68d67143f567b6c6199340c7f"

var excludedDirectories = []string{".git", ".hg", ".svn", "node_modules", "dist", "build", "coverage", ".venv", "vendor", "generated"}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type operationReport struct {
	Operation  string                `json:"operation"`
	DurationMS float64               `json:"duration_ms"`
	Manifest   manifestSummary       `json:"manifest"`
	Changes    []zoektadapter.Change `json:"changes,omitempty"`
	Stats      stateStats            `json:"stats"`
}

type manifestSummary struct {
	Generation uint64 `json:"generation"`
	Files      int    `json:"files"`
}

type stateStats struct {
	SourceFiles  int   `json:"source_files"`
	SourceBytes  int64 `json:"source_bytes"`
	IndexFiles   int   `json:"index_files"`
	IndexBytes   int64 `json:"index_bytes"`
	ZoektShards  int   `json:"zoekt_shards"`
	ManifestSize int64 `json:"manifest_bytes"`
}

type queryReport struct {
	Request       zoektadapter.Request  `json:"request"`
	Response      zoektadapter.Response `json:"response"`
	DurationMS    float64               `json:"duration_ms"`
	OracleMatches []zoektadapter.Match  `json:"oracle_matches,omitempty"`
	OracleEqual   *bool                 `json:"oracle_equal,omitempty"`
}

type benchmarkReport struct {
	Request    zoektadapter.Request `json:"request"`
	Iterations int                  `json:"iterations"`
	Successes  int                  `json:"successes"`
	MinMS      float64              `json:"min_ms"`
	MedianMS   float64              `json:"median_ms"`
	P95MS      float64              `json:"p95_ms"`
	MaxMS      float64              `json:"max_ms"`
	MeanMS     float64              `json:"mean_ms"`
}

type rgMessage struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Match struct {
				Text string `json:"text"`
			} `json:"match"`
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: search-eval <fixture|full|apply|reconcile|query|bench|stats>")
	}
	var err error
	switch os.Args[1] {
	case "fixture":
		err = runFixture(os.Args[2:])
	case "full":
		err = runFull(os.Args[2:])
	case "apply":
		err = runApply(os.Args[2:])
	case "reconcile":
		err = runReconcile(os.Args[2:])
	case "query":
		err = runQuery(os.Args[2:])
	case "bench":
		err = runBench(os.Args[2:])
	case "stats":
		err = runStats(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		var typed *zoektadapter.Error
		if errors.As(err, &typed) {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]any{
				"code": typed.Code, "message": typed.Message, "retryable": typed.Retryable,
			})
		}
		log.Fatal(err)
	}
}

func runFixture(args []string) error {
	flags := flag.NewFlagSet("fixture", flag.ContinueOnError)
	root := flags.String("root", "", "fixture root")
	generated := flags.Int("generated-files", 2000, "number of generated source files")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("--root is required")
	}
	if err := safeReset(*root); err != nil {
		return err
	}
	alpha := filepath.Join(*root, "alpha")
	beta := filepath.Join(*root, "beta")
	files := map[string]string{
		"README.md":                     "alpha_literal shared_marker\n",
		"src/router.ts":                 "export const immutableRoute = '/projects/alpha/mcp'; // shared_marker\n",
		"src/nested/handler.ts":         "export const route_042 = 'shared_marker';\n",
		"src/main.go":                   "package main\n// route_101 alpha_literal shared_marker\n",
		"src/lib.rs":                    "// route_202 rust_marker\n",
		"scripts/check.py":              "# route_303 python_marker\n",
		"native/parser.c":               "/* route_404 c_marker */\n",
		"native/parser.cpp":             "// route_505 cpp_marker\n",
		"docs/path with spaces/note.md": "space_path_marker\n",
		"docs/日本語/説明.md":                "unicode_path_marker\n",
		"data/config.json":              "{\"route_606\": true}\n",
	}
	for name, content := range files {
		if err := writeFile(alpha, name, content); err != nil {
			return err
		}
	}
	for _, directory := range excludedDirectories {
		if err := writeFile(alpha, directory+"/ignored.txt", "excluded_marker\n"); err != nil {
			return err
		}
	}
	for number := 0; number < *generated; number++ {
		name := fmt.Sprintf("corpus/source-%05d.ts", number)
		content := fmt.Sprintf("export const generated_%05d = 'bulk_marker_%05d';\n", number, number)
		if err := writeFile(alpha, name, content); err != nil {
			return err
		}
	}
	if err := writeFile(beta, "src/beta.ts", "export const beta_only_sentinel = true;\n"); err != nil {
		return err
	}
	if err := initializeDirtyGitFixture(alpha); err != nil {
		return err
	}
	if err := writeFile(alpha, "src/router.ts", files["src/router.ts"]+"// dirty_tracked_marker\n"); err != nil {
		return err
	}
	if err := writeFile(alpha, "src/untracked.ts", "export const untracked_marker = true;\n"); err != nil {
		return err
	}
	return encode(map[string]any{
		"root": *root, "generated_files": *generated, "zoekt_revision": zoektRevision,
		"go_version": runtime.Version(), "go_arch": runtime.GOARCH,
	})
}

func runFull(args []string) error {
	indexer, flags, err := parseIndexer("full", args)
	if err != nil {
		return err
	}
	_ = flags
	start := time.Now()
	manifest, err := indexer.Full(context.Background())
	if err != nil {
		return err
	}
	stats, err := collectStats(indexer.Workspace, indexer.IndexDir)
	if err != nil {
		return err
	}
	return encode(operationReport{Operation: "full", DurationMS: milliseconds(time.Since(start)), Manifest: summarizeManifest(manifest), Stats: stats})
}

func runApply(args []string) error {
	var values stringList
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	workspace := flags.String("workspace", "", "workspace root")
	indexDir := flags.String("index", "", "index directory")
	project := flags.String("project", "", "project ID")
	flags.Var(&values, "change", "path or path:delete; may be repeated")
	if err := flags.Parse(args); err != nil {
		return err
	}
	indexer, err := validateIndexer(*workspace, *indexDir, *project)
	if err != nil {
		return err
	}
	changes := make([]zoektadapter.Change, 0, len(values))
	for _, value := range values {
		name, deleted := strings.CutSuffix(value, ":delete")
		changes = append(changes, zoektadapter.Change{Path: name, Deleted: deleted})
	}
	start := time.Now()
	manifest, err := indexer.Apply(context.Background(), changes)
	if err != nil {
		return err
	}
	stats, err := collectStats(indexer.Workspace, indexer.IndexDir)
	if err != nil {
		return err
	}
	return encode(operationReport{Operation: "apply", DurationMS: milliseconds(time.Since(start)), Manifest: summarizeManifest(manifest), Changes: changes, Stats: stats})
}

func runReconcile(args []string) error {
	indexer, _, err := parseIndexer("reconcile", args)
	if err != nil {
		return err
	}
	start := time.Now()
	manifest, changes, err := indexer.Reconcile(context.Background())
	if err != nil {
		return err
	}
	stats, err := collectStats(indexer.Workspace, indexer.IndexDir)
	if err != nil {
		return err
	}
	return encode(operationReport{Operation: "reconcile", DurationMS: milliseconds(time.Since(start)), Manifest: summarizeManifest(manifest), Changes: changes, Stats: stats})
}

func runQuery(args []string) error {
	indexer, request, oracle, err := parseQuery("query", args)
	if err != nil {
		return err
	}
	start := time.Now()
	response, err := indexer.Search(context.Background(), request)
	if err != nil {
		return err
	}
	report := queryReport{Request: request, Response: response, DurationMS: milliseconds(time.Since(start))}
	if oracle != "" {
		report.OracleMatches, err = ripgrepOracle(context.Background(), oracle, indexer.Workspace, request)
		if err != nil {
			return err
		}
		equal := equalMatches(response.Matches, report.OracleMatches)
		report.OracleEqual = &equal
		if !equal {
			return fmt.Errorf("Zoekt and ripgrep results differ")
		}
	}
	return encode(report)
}

func runBench(args []string) error {
	flags := flag.NewFlagSet("bench", flag.ContinueOnError)
	workspace := flags.String("workspace", "", "workspace root")
	indexDir := flags.String("index", "", "index directory")
	project := flags.String("project", "", "project ID")
	pattern := flags.String("pattern", "", "search pattern")
	mode := flags.String("mode", "literal", "literal or regex")
	caseSensitive := flags.Bool("case-sensitive", true, "case-sensitive search")
	iterations := flags.Int("iterations", 100, "number of sequential queries")
	if err := flags.Parse(args); err != nil {
		return err
	}
	indexer, err := validateIndexer(*workspace, *indexDir, *project)
	if err != nil {
		return err
	}
	request := zoektadapter.Request{Pattern: *pattern, Mode: *mode, CaseSensitive: *caseSensitive, Limit: 100}
	session, err := indexer.OpenSession()
	if err != nil {
		return err
	}
	defer session.Close()
	if _, err := session.Search(context.Background(), request); err != nil {
		return err
	}
	durations := make([]time.Duration, 0, *iterations)
	var total time.Duration
	successes := 0
	for range *iterations {
		start := time.Now()
		_, queryErr := session.Search(context.Background(), request)
		duration := time.Since(start)
		durations = append(durations, duration)
		total += duration
		if queryErr == nil {
			successes++
		}
	}
	sort.Slice(durations, func(a, b int) bool { return durations[a] < durations[b] })
	report := benchmarkReport{
		Request: request, Iterations: *iterations, Successes: successes,
		MinMS: milliseconds(durations[0]), MedianMS: milliseconds(percentile(durations, 0.50)),
		P95MS: milliseconds(percentile(durations, 0.95)), MaxMS: milliseconds(durations[len(durations)-1]),
		MeanMS: milliseconds(total / time.Duration(len(durations))),
	}
	return encode(report)
}

func runStats(args []string) error {
	indexer, _, err := parseIndexer("stats", args)
	if err != nil {
		return err
	}
	stats, err := collectStats(indexer.Workspace, indexer.IndexDir)
	if err != nil {
		return err
	}
	return encode(stats)
}

func parseIndexer(name string, args []string) (zoektadapter.Indexer, *flag.FlagSet, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	workspace := flags.String("workspace", "", "workspace root")
	indexDir := flags.String("index", "", "index directory")
	project := flags.String("project", "", "project ID")
	if err := flags.Parse(args); err != nil {
		return zoektadapter.Indexer{}, flags, err
	}
	indexer, err := validateIndexer(*workspace, *indexDir, *project)
	return indexer, flags, err
}

func validateIndexer(workspace, indexDir, project string) (zoektadapter.Indexer, error) {
	if workspace == "" || indexDir == "" || project == "" {
		return zoektadapter.Indexer{}, errors.New("--workspace, --index, and --project are required")
	}
	return zoektadapter.Indexer{Workspace: workspace, IndexDir: indexDir, ProjectID: project}, nil
}

func parseQuery(name string, args []string) (zoektadapter.Indexer, zoektadapter.Request, string, error) {
	var globs stringList
	var languages stringList
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	workspace := flags.String("workspace", "", "workspace root")
	indexDir := flags.String("index", "", "index directory")
	project := flags.String("project", "", "project ID")
	pattern := flags.String("pattern", "", "search pattern")
	mode := flags.String("mode", "literal", "literal or regex")
	caseSensitive := flags.Bool("case-sensitive", true, "case-sensitive search")
	limit := flags.Int("limit", 50, "result limit")
	cursor := flags.String("cursor", "", "pagination cursor")
	oracle := flags.String("oracle", "", "ripgrep executable")
	flags.Var(&globs, "glob", "project-relative path glob; may be repeated")
	flags.Var(&languages, "language", "LCTK language identifier; may be repeated")
	if err := flags.Parse(args); err != nil {
		return zoektadapter.Indexer{}, zoektadapter.Request{}, "", err
	}
	indexer, err := validateIndexer(*workspace, *indexDir, *project)
	request := zoektadapter.Request{
		Pattern: *pattern, Mode: *mode, CaseSensitive: *caseSensitive, PathGlobs: globs,
		Languages: languages, Limit: *limit, Cursor: *cursor,
	}
	return indexer, request, *oracle, err
}

func ripgrepOracle(ctx context.Context, executable, workspace string, request zoektadapter.Request) ([]zoektadapter.Match, error) {
	arguments := []string{"--json", "--no-config", "--hidden", "--no-ignore", "--with-filename", "--line-number", "--column"}
	if request.Mode == "literal" || request.Mode == "" {
		arguments = append(arguments, "--fixed-strings")
	}
	if request.CaseSensitive {
		arguments = append(arguments, "--case-sensitive")
	} else {
		arguments = append(arguments, "--ignore-case")
	}
	for _, directory := range excludedDirectories {
		arguments = append(arguments, "--glob", "!"+directory+"/**")
	}
	for _, glob := range request.PathGlobs {
		arguments = append(arguments, "--glob", glob)
	}
	for _, language := range request.Languages {
		for _, extension := range languageExtensions(language) {
			arguments = append(arguments, "--glob", "**/*"+extension)
		}
	}
	arguments = append(arguments, "--", request.Pattern, ".")
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = workspace
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	var matches []zoektadapter.Match
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var message rgMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return nil, err
		}
		if message.Type != "match" {
			continue
		}
		line := strings.TrimSuffix(message.Data.Lines.Text, "\n")
		for _, submatch := range message.Data.Submatches {
			matches = append(matches, zoektadapter.Match{
				Path: normalizeRelativePath(message.Data.Path.Text), Line: message.Data.LineNumber,
				Column: submatch.Start + 1, Preview: boundedOraclePreview(line, submatch.Start, submatch.End),
				Match: submatch.Match.Text,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := command.Wait(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("ripgrep: %w: %s", err, stderr.String())
		}
	}
	sortMatches(matches)
	return matches, nil
}

func initializeDirtyGitFixture(root string) error {
	commands := [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.name", "LCTK Fixture"},
		{"config", "user.email", "fixture@invalid.example"},
		{"add", "."},
		{"commit", "-m", "fixture baseline"},
	}
	for _, arguments := range commands {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", arguments[0], err, output)
		}
	}
	return nil
}

func safeReset(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if filepath.Base(absolute) != "fixtures" {
		return fmt.Errorf("refusing to reset non-fixture path %q", absolute)
	}
	if err := os.RemoveAll(absolute); err != nil {
		return err
	}
	return os.MkdirAll(absolute, 0o755)
}

func writeFile(root, name, content string) error {
	absolute := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return err
	}
	return os.WriteFile(absolute, []byte(content), 0o644)
}

func collectStats(workspace, indexDir string) (stateStats, error) {
	var stats stateStats
	if err := filepath.WalkDir(workspace, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name != workspace && contains(excludedDirectories, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			stats.SourceFiles++
			stats.SourceBytes += info.Size()
		}
		return nil
	}); err != nil {
		return stats, err
	}
	if err := filepath.WalkDir(indexDir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			stats.IndexFiles++
			stats.IndexBytes += info.Size()
			if strings.HasSuffix(name, ".zoekt") {
				stats.ZoektShards++
			}
			if filepath.Base(name) == "lctk-manifest.json" {
				stats.ManifestSize = info.Size()
			}
		}
		return nil
	}); err != nil {
		return stats, err
	}
	return stats, nil
}

func equalMatches(left, right []zoektadapter.Match) bool {
	left = append([]zoektadapter.Match(nil), left...)
	right = append([]zoektadapter.Match(nil), right...)
	sortMatches(left)
	sortMatches(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if matchKey(left[index]) != matchKey(right[index]) {
			return false
		}
	}
	return true
}

func summarizeManifest(manifest zoektadapter.Manifest) manifestSummary {
	return manifestSummary{Generation: manifest.Generation, Files: len(manifest.Files)}
}

func normalizeRelativePath(name string) string {
	name = filepath.ToSlash(filepath.Clean(name))
	return strings.TrimPrefix(name, "./")
}

func sortMatches(matches []zoektadapter.Match) {
	sort.Slice(matches, func(a, b int) bool { return matchKey(matches[a]) < matchKey(matches[b]) })
}

func matchKey(match zoektadapter.Match) string {
	hash := sha256.Sum256([]byte(match.Match))
	return match.Path + "\x00" + strconv.Itoa(match.Line) + "\x00" + strconv.Itoa(match.Column) + "\x00" + hex.EncodeToString(hash[:])
}

func boundedOraclePreview(line string, start, end int) string {
	const maximum = 512
	if len(line) <= maximum {
		return line
	}
	windowStart := max(0, start-maximum/2)
	windowEnd := min(len(line), max(windowStart+maximum, end))
	windowStart = max(0, windowEnd-maximum)
	return line[windowStart:windowEnd]
}

func languageExtensions(language string) []string {
	switch strings.ToLower(language) {
	case "typescript", "ts":
		return []string{".ts", ".tsx"}
	case "javascript", "js":
		return []string{".js", ".jsx", ".mjs", ".cjs"}
	case "go", "golang":
		return []string{".go"}
	case "python", "py":
		return []string{".py"}
	case "rust", "rs":
		return []string{".rs"}
	case "c++", "cpp", "cxx":
		return []string{".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx"}
	default:
		return nil
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func percentile(sorted []time.Duration, fraction float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(float64(len(sorted)-1)*fraction)]
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration.Nanoseconds()) / float64(time.Millisecond)
}

func encode(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
