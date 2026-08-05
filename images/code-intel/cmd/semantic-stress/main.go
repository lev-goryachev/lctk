// semantic-stress measures the production exact-vector adapter at release
// scales without paying model inference time for unchanged deterministic data.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/semantic"
	"github.com/lev-goryachev/lctk/images/code-intel/internal/symbols"
)

type emptySource struct{}

func (emptySource) ReadProjectFile(string, int64) ([]byte, string, error) {
	return nil, "", fmt.Errorf("stress corpus does not read project files")
}

type fixedEmbedder struct{ dimensions int }

func (embedder fixedEmbedder) Embed(_ context.Context, _ semantic.EmbeddingKind, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for index := range result {
		result[index] = make([]float32, embedder.dimensions)
		result[index][0] = 1
	}
	return result, nil
}

type measurement struct {
	Files             int     `json:"files"`
	Dimensions        int     `json:"dimensions"`
	PopulateSeconds   float64 `json:"populate_seconds"`
	QueryMilliseconds float64 `json:"query_milliseconds"`
	DatabaseBytes     int64   `json:"database_bytes"`
	HeapBytes         uint64  `json:"heap_bytes"`
	Matches           int     `json:"matches"`
	Total             int     `json:"total"`
}

func main() {
	countsFlag := flag.String("counts", "1000,10000,100000,1000000", "comma-separated corpus sizes")
	root := flag.String("root", "", "directory for disposable stress databases")
	reuse := flag.Bool("reuse", false, "query existing stress databases without repopulating them")
	flag.Parse()
	if *root == "" {
		fatal(fmt.Errorf("--root is required"))
	}
	counts, err := parseCounts(*countsFlag)
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(*root, 0o700); err != nil {
		fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	for _, count := range counts {
		result, err := measure(context.Background(), *root, count, *reuse)
		if err != nil {
			fatal(err)
		}
		if err := encoder.Encode(result); err != nil {
			fatal(err)
		}
	}
}

func measure(ctx context.Context, root string, count int, reuse bool) (measurement, error) {
	directory := filepath.Join(root, strconv.Itoa(count))
	if !reuse {
		if err := os.RemoveAll(directory); err != nil {
			return measurement{}, err
		}
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return measurement{}, err
		}
	}
	database := filepath.Join(directory, "semantic.db")
	const dimensions = 768
	store, err := semantic.Open(semantic.Config{Path: database, Model: "stress-model", Dimensions: dimensions},
		emptySource{}, nilOutliner{}, fixedEmbedder{dimensions: dimensions})
	if err != nil {
		return measurement{}, err
	}
	var populateDuration time.Duration
	if !reuse {
		started := time.Now()
		if err := store.PopulateStressCorpus(ctx, count); err != nil {
			store.Close()
			return measurement{}, err
		}
		populateDuration = time.Since(started)
	}
	started := time.Now()
	response, err := store.Search(ctx, semantic.Request{Query: "synthetic stress symbol", Limit: 20})
	queryDuration := time.Since(started)
	if err != nil {
		store.Close()
		return measurement{}, err
	}
	if response.Total != count {
		store.Close()
		return measurement{}, fmt.Errorf("semantic total = %d, want %d", response.Total, count)
	}
	expectedMatches := min(count, 20)
	if len(response.Matches) != expectedMatches || response.Truncated != (count > expectedMatches) {
		store.Close()
		return measurement{}, fmt.Errorf("semantic result has %d matches and truncated=%t", len(response.Matches), response.Truncated)
	}
	if err := store.Close(); err != nil {
		return measurement{}, err
	}
	info, err := os.Stat(database)
	if err != nil {
		return measurement{}, err
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return measurement{
		Files: count, Dimensions: dimensions, PopulateSeconds: populateDuration.Seconds(),
		QueryMilliseconds: float64(queryDuration.Microseconds()) / 1000,
		DatabaseBytes:     info.Size(), HeapBytes: memory.HeapAlloc, Matches: len(response.Matches), Total: response.Total,
	}, nil
}

type nilOutliner struct{}

func (nilOutliner) Outline(context.Context, string, []byte, string) (symbols.Outline, error) {
	return symbols.Outline{}, nil
}

func parseCounts(value string) ([]int, error) {
	var counts []int
	for _, field := range strings.Split(value, ",") {
		count, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || count < 1 || count > 1_000_000 {
			return nil, fmt.Errorf("invalid stress count %q", field)
		}
		counts = append(counts, count)
	}
	if len(counts) == 0 {
		return nil, fmt.Errorf("at least one stress count is required")
	}
	return counts, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "semantic-stress:", err)
	os.Exit(1)
}
