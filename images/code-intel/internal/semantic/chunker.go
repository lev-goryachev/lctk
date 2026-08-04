package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/symbols"
)

const (
	// Three KiB keeps token-dense source comfortably below a 4096-token slot after
	// path and structural context are added.
	defaultChunkBytes   = 3 << 10
	defaultOverlapLines = 8
)

// Outliner is the structural capability the chunker needs. Keeping the narrow
// interface makes chunking independently testable while reusing Stage 4 byte
// extents in production.
type Outliner interface {
	Outline(ctx context.Context, name string, content []byte, digest string) (symbols.Outline, error)
}

// Chunk is one durable retrieval unit. StableID describes its structural place;
// ContentDigest decides whether its embedding may be reused.
type Chunk struct {
	StableID      string
	Path          string
	Language      string
	Precision     string
	Anchor        string
	Ordinal       int
	StartLine     int
	EndLine       int
	Content       string
	EmbeddingText string
	ContentDigest string
}

// Chunker turns one already-authorized file into bounded retrieval units.
type Chunker struct {
	Outliner     Outliner
	MaxBytes     int
	OverlapLines int
}

// Chunks prefers top-level syntax declarations and uses an explicit text
// fallback for unsupported files. Parse failures are not hidden as text chunks:
// a supported file that cannot be parsed must remain diagnosable.
func (c Chunker) Chunks(ctx context.Context, path string, content []byte, digest string) ([]Chunk, error) {
	maximum := c.MaxBytes
	if maximum <= 0 {
		maximum = defaultChunkBytes
	}
	overlap := c.OverlapLines
	if overlap < 0 {
		overlap = 0
	}
	if overlap == 0 {
		overlap = defaultOverlapLines
	}
	if c.Outliner != nil {
		outline, err := c.Outliner.Outline(ctx, path, content, digest)
		if err == nil {
			chunks := structuralChunks(path, content, outline, maximum)
			if len(chunks) > 0 {
				return chunks, nil
			}
			return textChunks(path, content, outline.Language, maximum, overlap), nil
		}
		var typed *symbols.Error
		if !errors.As(err, &typed) || typed.Code != symbols.CodeUnsupportedLanguage {
			return nil, err
		}
	}
	return textChunks(path, content, "text", maximum, overlap), nil
}

func structuralChunks(path string, content []byte, outline symbols.Outline, maximum int) []Chunk {
	var chunks []Chunk
	type group struct {
		startByte int
		endByte   int
		startLine int
		endLine   int
		anchor    string
	}
	var current group
	flush := func() {
		if current.endByte <= current.startByte {
			return
		}
		chunks = append(chunks, makeChunk(path, outline.Language, "syntax", current.anchor,
			0, current.startLine, current.endLine, content[current.startByte:current.endByte]))
		current = group{}
	}
	for _, symbol := range outline.Symbols {
		if symbol.Depth != 0 || symbol.StartByte < 0 || symbol.EndByte > len(content) || symbol.EndByte <= symbol.StartByte {
			continue
		}
		anchor := string(symbol.Kind) + ":" + symbol.Name
		if symbol.EndByte-symbol.StartByte <= maximum {
			if current.endByte == 0 {
				current = group{startByte: symbol.StartByte, endByte: symbol.EndByte,
					startLine: symbol.StartLine, endLine: symbol.EndLine, anchor: anchor}
				continue
			}
			if symbol.EndByte-current.startByte <= maximum {
				current.endByte = symbol.EndByte
				current.endLine = symbol.EndLine
				continue
			}
			flush()
			current = group{startByte: symbol.StartByte, endByte: symbol.EndByte,
				startLine: symbol.StartLine, endLine: symbol.EndLine, anchor: anchor}
			continue
		}
		flush()
		pieces := splitLines(content[symbol.StartByte:symbol.EndByte], maximum, 0)
		line := symbol.StartLine
		for ordinal, piece := range pieces {
			end := line + lineCount(piece) - 1
			chunks = append(chunks, makeChunk(path, outline.Language, "syntax", anchor,
				ordinal, line, end, piece))
			line = end + 1
		}
	}
	flush()
	return chunks
}

func textChunks(path string, content []byte, language string, maximum, overlap int) []Chunk {
	pieces := splitLines(content, maximum, overlap)
	chunks := make([]Chunk, 0, len(pieces))
	line := 1
	for ordinal, piece := range pieces {
		end := line + lineCount(piece) - 1
		chunks = append(chunks, makeChunk(path, language, "text", "file", ordinal, line, end, piece))
		advance := lineCount(piece) - overlap
		if advance < 1 {
			advance = 1
		}
		line += advance
	}
	return chunks
}

func makeChunk(path, language, precision, anchor string, ordinal, startLine, endLine int, content []byte) Chunk {
	stable := digestString(fmt.Sprintf("%s\x00%s\x00%d", path, anchor, ordinal))
	text := strings.TrimSpace(string(content))
	return Chunk{
		StableID: stable, Path: path, Language: language, Precision: precision,
		Anchor: anchor, Ordinal: ordinal, StartLine: startLine, EndLine: endLine,
		Content: text, EmbeddingText: path + "\n" + anchor + "\n" + text,
		ContentDigest: digestString(text),
	}
}

// splitLines keeps UTF-8 source on line boundaries. A single overlong line is
// split on rune boundaries so every chunk remains bounded and valid UTF-8.
func splitLines(content []byte, maximum, overlap int) [][]byte {
	if len(content) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(content), "\n")
	var pieces [][]byte
	for start := 0; start < len(lines); {
		end := start
		size := 0
		for end < len(lines) && (size+len(lines[end]) <= maximum || end == start) {
			if len(lines[end]) > maximum && end == start {
				for _, part := range splitUTF8([]byte(lines[end]), maximum) {
					pieces = append(pieces, part)
				}
				end++
				size = 0
				break
			}
			size += len(lines[end])
			end++
		}
		if size > 0 {
			pieces = append(pieces, []byte(strings.Join(lines[start:end], "")))
		}
		if end >= len(lines) {
			break
		}
		next := end - overlap
		if next <= start {
			next = start + 1
		}
		start = next
	}
	return pieces
}

func splitUTF8(content []byte, maximum int) [][]byte {
	var parts [][]byte
	for len(content) > maximum {
		cut := maximum
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}
		if cut == 0 {
			cut = maximum
		}
		parts = append(parts, append([]byte(nil), content[:cut]...))
		content = content[cut:]
	}
	if len(content) > 0 {
		parts = append(parts, append([]byte(nil), content...))
	}
	return parts
}

func lineCount(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := strings.Count(string(content), "\n")
	if content[len(content)-1] != '\n' {
		count++
	}
	return count
}

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
