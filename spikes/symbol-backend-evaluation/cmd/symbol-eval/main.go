// Command symbol-eval measures two candidate symbol engines against one corpus
// of real source.
//
// It is evidence only. It is not production LCTK code, it defines no public tool
// schema, and the queries it carries are the ones a production symbol layer would
// use so that the measurement is of what would ship.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/spikes/symbol-backend-evaluation/corpus"
	"github.com/lev-goryachev/lctk/spikes/symbol-backend-evaluation/engines"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "corpus":
		err = runCorpus(os.Args[2:])
	case "measure":
		err = runMeasure(os.Args[2:])
	case "broken":
		err = runBroken(os.Args[2:])
	case "compare":
		err = runCompare(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "symbol-eval:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: symbol-eval <command> [flags]

  corpus   clone the pinned upstream projects
  measure  run one engine over the corpus and report cost and coverage
  compare  run both engines and report where they disagree
  broken   report whether each engine can tell a broken file from a whole one
`)
}

func runCorpus(args []string) error {
	flags := flag.NewFlagSet("corpus", flag.ExitOnError)
	dir := flags.String("dir", "/evidence/corpus", "where to clone the pinned projects")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resolved, err := corpus.Fetch(*dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(resolved))
	for name := range resolved {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("%-10s %s\n", name, resolved[name])
	}
	return nil
}

// engineFor builds a candidate by name.
//
// The same budget bounds both candidates, so neither is credited with finishing a
// file the other was stopped on.
func engineFor(name, ctagsBinary string, ctagsMode engines.Mode, budget time.Duration) (engines.Engine, error) {
	switch name {
	case "tree-sitter":
		engine, err := engines.NewTreeSitter()
		if err != nil {
			return nil, err
		}
		engine.Budget = budget
		return engine, nil
	case "ctags", "universal-ctags":
		return engines.NewCtags(ctagsBinary, ctagsMode, budget)
	}
	return nil, fmt.Errorf("unknown engine %q", name)
}

// LanguageReport is one language's outcome for one engine.
type LanguageReport struct {
	Language string `json:"language"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	Symbols  int    `json:"symbols"`
	// Nested counts symbols reported inside another declaration, which is the
	// evidence that containment works rather than merely being claimed.
	Nested int `json:"nested"`
	// WithByteRange counts symbols bounded in bytes rather than only in lines.
	WithByteRange int `json:"with_byte_range"`
	// WithEndLine counts symbols whose end line differs from the start, which is
	// what a caller needs to show a declaration rather than a cursor position.
	WithEndLine int `json:"with_end_line"`
	// FilesWithoutSymbols is how many files produced nothing. A high number is
	// either a language the engine handles badly or a query with a gap in it.
	FilesWithoutSymbols int `json:"files_without_symbols"`
	Unparsed            int `json:"unparsed_files"`
	Failed              int `json:"failed_files"`
	// TimedOut counts files abandoned when the per-file budget ran out, and
	// Skipped counts files never offered to the engine because of the size limit.
	// Both are reported rather than folded into the totals, because a file nobody
	// analysed is not a file with no symbols.
	TimedOut     int `json:"timed_out_files"`
	Skipped      int `json:"skipped_files"`
	Milliseconds int `json:"milliseconds"`
	// SlowestFile and SlowestMilliseconds name the worst single file, which is the
	// figure that decides whether a budget is needed at all.
	SlowestFile         string `json:"slowest_file,omitempty"`
	SlowestMilliseconds int    `json:"slowest_milliseconds"`
}

// Report is one engine's whole run.
type Report struct {
	Engine       string               `json:"engine"`
	Capabilities engines.Capabilities `json:"capabilities"`
	Languages    []LanguageReport     `json:"languages"`
	TotalFiles   int                  `json:"total_files"`
	TotalBytes   int64                `json:"total_bytes"`
	TotalSymbols int                  `json:"total_symbols"`
	Milliseconds int                  `json:"milliseconds"`
	// PeakRSSKiB is this process's high-water mark. Each engine is measured in its
	// own process so the figure is attributable; for ctags it includes the child.
	PeakRSSKiB int `json:"peak_rss_kib"`
}

func collectCorpus(dir, goRoot string) ([]corpus.File, error) {
	extra := map[string][]string{}
	if goRoot != "" {
		extra["go"] = []string{goRoot}
	}
	return corpus.Collect(dir, extra)
}

func runMeasure(args []string) error {
	flags := flag.NewFlagSet("measure", flag.ExitOnError)
	dir := flags.String("corpus", "/evidence/corpus", "corpus directory")
	goRoot := flags.String("go-root", "", "an extra root measured as Go, normally the LCTK checkout")
	engineName := flags.String("engine", "tree-sitter", "tree-sitter or ctags")
	ctagsBinary := flags.String("ctags", "ctags", "the ctags executable")
	ctagsMode := flags.String("ctags-mode", "per-file", "how ctags is driven: per-file or interactive")
	budget := flags.Duration("budget", 0, "per-file analysis budget; 0 is unbounded")
	maxBytes := flags.Int("max-bytes", 0, "skip files larger than this; 0 is unlimited")
	only := flags.String("language", "", "measure one language only, for narrowing a finding to a subset")
	verbose := flags.Bool("verbose", false, "name each file before analysing it, so a hang is attributable")
	asJSON := flags.Bool("json", false, "emit the report as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}

	files, err := collectCorpus(*dir, *goRoot)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("the corpus is empty; run `symbol-eval corpus` first")
	}

	engine, err := engineFor(*engineName, *ctagsBinary, engines.Mode(*ctagsMode), *budget)
	if err != nil {
		return err
	}
	defer engine.Close()

	report := Report{Engine: engine.Name(), Capabilities: engine.Capabilities()}
	byLanguage := map[string]*LanguageReport{}
	started := time.Now()

	for _, file := range files {
		if *only != "" && file.Language != *only {
			continue
		}
		content, err := os.ReadFile(file.Full)
		if err != nil {
			continue
		}
		language := byLanguage[file.Language]
		if language == nil {
			language = &LanguageReport{Language: file.Language}
			byLanguage[file.Language] = language
		}
		if *maxBytes > 0 && len(content) > *maxBytes {
			language.Files++
			language.Bytes += int64(len(content))
			language.Skipped++
			continue
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "%s %s (%d bytes)\n", file.Language, file.Path, len(content))
		}
		fileStarted := time.Now()
		result := engine.Analyse(engines.Request{Path: file.Path, Full: file.Full, Language: file.Language}, content)
		elapsed := time.Since(fileStarted)

		language.Files++
		language.Bytes += int64(len(content))
		language.Milliseconds += int(elapsed.Milliseconds())
		if int(elapsed.Milliseconds()) > language.SlowestMilliseconds {
			language.SlowestMilliseconds = int(elapsed.Milliseconds())
			language.SlowestFile = file.Path
		}
		if result.TimedOut {
			language.TimedOut++
			continue
		}
		if result.Err != "" {
			language.Failed++
			continue
		}
		if !result.Parsed {
			language.Unparsed++
		}
		if len(result.Symbols) == 0 {
			language.FilesWithoutSymbols++
		}
		language.Symbols += len(result.Symbols)
		for _, symbol := range result.Symbols {
			if symbol.Container != "" {
				language.Nested++
			}
			if symbol.EndByte > symbol.StartByte {
				language.WithByteRange++
			}
			if symbol.EndLine > symbol.StartLine {
				language.WithEndLine++
			}
		}
	}
	report.Milliseconds = int(time.Since(started).Milliseconds())

	names := make([]string, 0, len(byLanguage))
	for name := range byLanguage {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		language := *byLanguage[name]
		report.Languages = append(report.Languages, language)
		report.TotalFiles += language.Files
		report.TotalBytes += language.Bytes
		report.TotalSymbols += language.Symbols
	}
	report.PeakRSSKiB = peakRSSKiB()

	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	printReport(report)
	return nil
}

func printReport(report Report) {
	fmt.Printf("engine: %s\n", report.Engine)
	fmt.Printf("capabilities: byte_ranges=%v containment=%v syntax_validity=%v in_process=%v license=%s\n",
		report.Capabilities.ByteRanges, report.Capabilities.Containment,
		report.Capabilities.SyntaxValidity, report.Capabilities.InProcess, report.Capabilities.License)
	fmt.Printf("\n%-11s %6s %8s %8s %8s %8s %8s %6s %6s %6s %6s %6s %7s\n",
		"language", "files", "KiB", "symbols", "nested", "byterng", "endline",
		"nosym", "unprs", "toobig", "budget", "failed", "ms")
	for _, language := range report.Languages {
		fmt.Printf("%-11s %6d %8d %8d %8d %8d %8d %6d %6d %6d %6d %6d %7d\n",
			language.Language, language.Files, language.Bytes/1024, language.Symbols,
			language.Nested, language.WithByteRange, language.WithEndLine,
			language.FilesWithoutSymbols, language.Unparsed, language.Skipped,
			language.TimedOut, language.Failed, language.Milliseconds)
	}
	fmt.Println("\nslowest file per language:")
	for _, language := range report.Languages {
		if language.SlowestFile == "" {
			continue
		}
		fmt.Printf("  %-11s %5d ms  %s\n", language.Language,
			language.SlowestMilliseconds, language.SlowestFile)
	}
	fmt.Printf("\ntotal: %d files, %d KiB, %d symbols in %d ms, peak RSS %d KiB\n",
		report.TotalFiles, report.TotalBytes/1024, report.TotalSymbols,
		report.Milliseconds, report.PeakRSSKiB)
}

// peakRSSKiB reads the process high-water mark from the kernel. It is Linux-only,
// which the whole harness is.
func peakRSSKiB() int {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0
		}
		return value
	}
	return 0
}

// Disagreement is one file where the two engines named different symbols.
type Disagreement struct {
	Path            string   `json:"path"`
	Language        string   `json:"language"`
	OnlyTreeSitter  []string `json:"only_tree_sitter,omitempty"`
	OnlyCtags       []string `json:"only_ctags,omitempty"`
	CommonCount     int      `json:"common"`
	TreeSitterCount int      `json:"tree_sitter_total"`
	CtagsCount      int      `json:"ctags_total"`
}

func runCompare(args []string) error {
	flags := flag.NewFlagSet("compare", flag.ExitOnError)
	dir := flags.String("corpus", "/evidence/corpus", "corpus directory")
	goRoot := flags.String("go-root", "", "an extra root measured as Go")
	ctagsBinary := flags.String("ctags", "ctags", "the ctags executable")
	ctagsMode := flags.String("ctags-mode", "per-file", "how ctags is driven: per-file or interactive")
	budget := flags.Duration("budget", 0, "per-file analysis budget; 0 is unbounded")
	limit := flags.Int("examples", 3, "how many example disagreements to print per language")
	if err := flags.Parse(args); err != nil {
		return err
	}

	files, err := collectCorpus(*dir, *goRoot)
	if err != nil {
		return err
	}
	tree, err := engineFor("tree-sitter", "", "", *budget)
	if err != nil {
		return err
	}
	defer tree.Close()
	tags, err := engineFor("ctags", *ctagsBinary, engines.Mode(*ctagsMode), *budget)
	if err != nil {
		return err
	}
	defer tags.Close()

	type totals struct {
		files, common, onlyTree, onlyTags int
		examples                          []Disagreement
	}
	byLanguage := map[string]*totals{}

	for _, file := range files {
		content, err := os.ReadFile(file.Full)
		if err != nil {
			continue
		}
		left := tree.Analyse(engines.Request{Path: file.Path, Full: file.Full, Language: file.Language}, content)
		right := tags.Analyse(engines.Request{Path: file.Path, Full: file.Full, Language: file.Language}, content)
		if left.Err != "" || right.Err != "" {
			continue
		}
		leftNames := nameSet(left.Symbols)
		rightNames := nameSet(right.Symbols)

		aggregate := byLanguage[file.Language]
		if aggregate == nil {
			aggregate = &totals{}
			byLanguage[file.Language] = aggregate
		}
		aggregate.files++

		onlyLeft := difference(leftNames, rightNames)
		onlyRight := difference(rightNames, leftNames)
		common := len(leftNames) - len(onlyLeft)
		aggregate.common += common
		aggregate.onlyTree += len(onlyLeft)
		aggregate.onlyTags += len(onlyRight)

		if (len(onlyLeft) > 0 || len(onlyRight) > 0) && len(aggregate.examples) < *limit {
			aggregate.examples = append(aggregate.examples, Disagreement{
				Path:            file.Path,
				Language:        file.Language,
				OnlyTreeSitter:  head(onlyLeft, 6),
				OnlyCtags:       head(onlyRight, 6),
				CommonCount:     common,
				TreeSitterCount: len(leftNames),
				CtagsCount:      len(rightNames),
			})
		}
	}

	names := make([]string, 0, len(byLanguage))
	for name := range byLanguage {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Printf("%-11s %6s %8s %14s %10s\n", "language", "files", "common", "tree-sitter", "ctags")
	for _, name := range names {
		aggregate := byLanguage[name]
		fmt.Printf("%-11s %6d %8d %14d %10d\n",
			name, aggregate.files, aggregate.common, aggregate.onlyTree, aggregate.onlyTags)
	}
	fmt.Println("\nthe last two columns are names one engine found and the other did not")
	for _, name := range names {
		for _, example := range byLanguage[name].examples {
			fmt.Printf("\n%s (%s): tree-sitter %d, ctags %d, common %d\n",
				example.Path, name, example.TreeSitterCount, example.CtagsCount, example.CommonCount)
			if len(example.OnlyTreeSitter) > 0 {
				fmt.Printf("  only tree-sitter: %s\n", strings.Join(example.OnlyTreeSitter, ", "))
			}
			if len(example.OnlyCtags) > 0 {
				fmt.Printf("  only ctags:       %s\n", strings.Join(example.OnlyCtags, ", "))
			}
		}
	}
	return nil
}

func nameSet(symbols []engines.Symbol) map[string]struct{} {
	set := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		set[symbol.Name] = struct{}{}
	}
	return set
}

func difference(from, without map[string]struct{}) []string {
	var only []string
	for name := range from {
		if _, present := without[name]; !present {
			only = append(only, name)
		}
	}
	sort.Strings(only)
	return only
}

func head(values []string, count int) []string {
	if len(values) <= count {
		return values
	}
	return values[:count]
}

// brokenCases are whole files and the same files damaged.
//
// The damage is the kind an agent actually encounters: a half-written edit. Each
// case is a real construct truncated mid-body, not random bytes, because random
// bytes are easy and a truncated function is the case that matters.
var brokenCases = []struct {
	language string
	name     string
	whole    string
	broken   string
}{
	{
		language: "go", name: "a.go",
		whole:  "package p\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
		broken: "package p\n\nfunc Add(a, b int) int {\n\treturn a +\n",
	},
	{
		language: "python", name: "a.py",
		whole:  "class C:\n    def m(self):\n        return 1\n",
		broken: "class C:\n    def m(self:\n        return 1\n",
	},
	{
		language: "rust", name: "a.rs",
		whole:  "pub struct S { pub f: u32 }\n\nimpl S {\n    pub fn get(&self) -> u32 { self.f }\n}\n",
		broken: "pub struct S { pub f: u32 }\n\nimpl S {\n    pub fn get(&self) -> u32 { self.f\n",
	},
	{
		language: "c", name: "a.c",
		whole:  "struct S { int f; };\n\nint add(int a, int b) {\n\treturn a + b;\n}\n",
		broken: "struct S { int f; };\n\nint add(int a, int b) {\n\treturn a +\n",
	},
	{
		language: "cpp", name: "a.cpp",
		whole:  "namespace n {\nclass C {\npublic:\n  int m() { return 1; }\n};\n}\n",
		broken: "namespace n {\nclass C {\npublic:\n  int m() { return 1;\n",
	},
	{
		language: "javascript", name: "a.js",
		whole:  "export function f(a) {\n  return a + 1;\n}\n",
		broken: "export function f(a) {\n  return a +\n",
	},
	{
		language: "typescript", name: "a.ts",
		whole:  "export interface I { f: number }\n\nexport function g(a: I): number {\n  return a.f;\n}\n",
		broken: "export interface I { f: number\n\nexport function g(a: I): number {\n  return a.f;\n",
	},
}

func runBroken(args []string) error {
	flags := flag.NewFlagSet("broken", flag.ExitOnError)
	ctagsBinary := flags.String("ctags", "ctags", "the ctags executable")
	ctagsMode := flags.String("ctags-mode", "per-file", "how ctags is driven: per-file or interactive")
	budget := flags.Duration("budget", 0, "per-file analysis budget; 0 is unbounded")
	if err := flags.Parse(args); err != nil {
		return err
	}

	tree, err := engineFor("tree-sitter", "", "", *budget)
	if err != nil {
		return err
	}
	defer tree.Close()
	tags, err := engineFor("ctags", *ctagsBinary, engines.Mode(*ctagsMode), *budget)
	if err != nil {
		return err
	}
	defer tags.Close()

	fmt.Printf("%-11s %-24s %-24s\n", "language", "tree-sitter", "universal-ctags")
	fmt.Printf("%-11s %-24s %-24s\n", "", "whole -> broken", "whole -> broken")
	distinguished, blind := 0, 0
	for _, testCase := range brokenCases {
		wholeLeft := tree.Analyse(engines.Request{Path: testCase.name, Language: testCase.language}, []byte(testCase.whole))
		brokenLeft := tree.Analyse(engines.Request{Path: testCase.name, Language: testCase.language}, []byte(testCase.broken))
		wholeRight := tags.Analyse(engines.Request{Path: testCase.name, Language: testCase.language}, []byte(testCase.whole))
		brokenRight := tags.Analyse(engines.Request{Path: testCase.name, Language: testCase.language}, []byte(testCase.broken))

		if wholeLeft.Parsed && !brokenLeft.Parsed {
			distinguished++
		}
		if wholeRight.Parsed && brokenRight.Parsed {
			blind++
		}
		fmt.Printf("%-11s %-24s %-24s\n", testCase.language,
			fmt.Sprintf("parsed %v -> %v", wholeLeft.Parsed, brokenLeft.Parsed),
			fmt.Sprintf("parsed %v -> %v", wholeRight.Parsed, brokenRight.Parsed))
	}
	fmt.Printf("\ntree-sitter distinguished %d of %d damaged files\n", distinguished, len(brokenCases))
	fmt.Printf("universal-ctags reported %d of %d damaged files as whole\n", blind, len(brokenCases))
	return nil
}
