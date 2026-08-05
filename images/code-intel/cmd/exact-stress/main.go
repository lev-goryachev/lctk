// exact-stress measures the production filesystem inventory and Zoekt adapter
// with real distinct paths through the one-million-file release target.
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
	"sync"
	"sync/atomic"
	"time"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/searchindex"
)

type measurement struct {
	Files              int     `json:"files"`
	GenerateSeconds    float64 `json:"generate_seconds"`
	IndexSeconds       float64 `json:"index_seconds"`
	QueryMilliseconds  float64 `json:"query_milliseconds"`
	IndexBytes         int64   `json:"index_bytes"`
	HeapBytes          uint64  `json:"heap_bytes"`
	Matches            int     `json:"matches"`
	Truncated          bool    `json:"truncated"`
	PublishedFileCount int     `json:"published_file_count"`
}

func main() {
	countsFlag := flag.String("counts", "1000,10000,100000,1000000", "comma-separated corpus sizes")
	corpusRoot := flag.String("corpus-root", "", "directory for disposable source corpora")
	stateRoot := flag.String("state-root", "", "directory for disposable production index state")
	reuseCorpus := flag.Bool("reuse-corpus", false, "reuse already generated ordinary source files")
	flag.Parse()
	if *corpusRoot == "" || *stateRoot == "" {
		fatal(fmt.Errorf("--corpus-root and --state-root are required"))
	}
	counts, err := parseCounts(*countsFlag)
	if err != nil {
		fatal(err)
	}
	for _, root := range []string{*corpusRoot, *stateRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			fatal(err)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	for _, count := range counts {
		result, err := measure(context.Background(), *corpusRoot, *stateRoot, count, *reuseCorpus)
		if err != nil {
			fatal(err)
		}
		if err := encoder.Encode(result); err != nil {
			fatal(err)
		}
	}
}

func measure(ctx context.Context, corpusRoot, stateRoot string, count int, reuseCorpus bool) (measurement, error) {
	corpusDirectory := filepath.Join(corpusRoot, strconv.Itoa(count))
	stateDirectory := filepath.Join(stateRoot, strconv.Itoa(count))
	if err := os.RemoveAll(stateDirectory); err != nil {
		return measurement{}, err
	}
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return measurement{}, err
	}
	workspace := filepath.Join(corpusDirectory, "workspace")
	commonContent := []byte("package stress\nfunc CommonMarker() {}\n")
	var generateDuration time.Duration
	if reuseCorpus {
		info, err := os.Stat(workspace)
		if err != nil || !info.IsDir() {
			return measurement{}, fmt.Errorf("reuse corpus %d is unavailable", count)
		}
	} else {
		if err := os.RemoveAll(corpusDirectory); err != nil {
			return measurement{}, err
		}
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			return measurement{}, err
		}
		started := time.Now()
		for directoryIndex := 0; directoryIndex <= (count-1)/10_000; directoryIndex++ {
			if err := os.MkdirAll(filepath.Join(workspace, fmt.Sprintf("%03d", directoryIndex)), 0o700); err != nil {
				return measurement{}, err
			}
		}
		workers := min(runtime.GOMAXPROCS(0)*4, 64)
		var next atomic.Int64
		var firstErr error
		var errorOnce sync.Once
		var wait sync.WaitGroup
		for range workers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				for {
					index := int(next.Add(1) - 1)
					if index >= count {
						return
					}
					if err := ctx.Err(); err != nil {
						errorOnce.Do(func() { firstErr = err })
						return
					}
					path := filepath.Join(workspace, fmt.Sprintf("%03d", index/10_000), fmt.Sprintf("file-%07d.go", index))
					var err error
					if index == count-1 {
						err = os.WriteFile(path, []byte("package stress\nfunc UniqueReleaseTarget() {}\n"), 0o600)
					} else {
						err = os.WriteFile(path, commonContent, 0o600)
					}
					if err != nil {
						errorOnce.Do(func() { firstErr = fmt.Errorf("create corpus path %d: %w", index, err) })
						return
					}
				}
			}()
		}
		wait.Wait()
		if firstErr != nil {
			return measurement{}, firstErr
		}
		generateDuration = time.Since(started)
	}
	store := searchindex.New(workspace, filepath.Join(stateDirectory, "index"), "stress",
		searchindex.Limits{Parallelism: runtime.NumCPU()})
	defer store.Close()
	started := time.Now()
	state, err := store.Rebuild(ctx)
	indexDuration := time.Since(started)
	if err != nil {
		return measurement{}, err
	}
	if state.FileCount != count {
		return measurement{}, fmt.Errorf("published file count = %d, want %d", state.FileCount, count)
	}
	started = time.Now()
	response, err := store.Search(ctx, searchindex.Request{Pattern: "UniqueReleaseTarget", Mode: searchindex.ModeLiteral, Limit: 20})
	queryDuration := time.Since(started)
	if err != nil {
		return measurement{}, err
	}
	if len(response.Matches) != 1 || response.Truncated {
		return measurement{}, fmt.Errorf("unique exact query returned %d matches with truncated=%t", len(response.Matches), response.Truncated)
	}
	indexBytes, err := store.DiskBytes()
	if err != nil {
		return measurement{}, err
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return measurement{
		Files: count, GenerateSeconds: generateDuration.Seconds(), IndexSeconds: indexDuration.Seconds(),
		QueryMilliseconds: float64(queryDuration.Microseconds()) / 1000, IndexBytes: indexBytes,
		HeapBytes: memory.HeapAlloc, Matches: len(response.Matches), Truncated: response.Truncated,
		PublishedFileCount: state.FileCount,
	}, nil
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
	fmt.Fprintln(os.Stderr, "exact-stress:", err)
	os.Exit(1)
}
