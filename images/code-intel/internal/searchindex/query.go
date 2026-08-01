package searchindex

import (
	"fmt"
	"path/filepath"
	"regexp/syntax"
	"strings"
	"unicode/utf8"

	"github.com/sourcegraph/zoekt/query"
)

// syntaxFlags match how the engine parses patterns elsewhere, so a regex that
// works in one place works in the other.
const syntaxFlags = syntax.ClassNL | syntax.PerlX | syntax.UnicodeGroups

func buildQuery(request Request) (query.Q, error) {
	var content query.Q
	switch strings.ToLower(strings.TrimSpace(request.Mode)) {
	case "", ModeLiteral:
		content = &query.Substring{
			Pattern:       request.Pattern,
			CaseSensitive: request.CaseSensitive,
			Content:       true,
		}
	case ModeRegex:
		parsed, err := syntax.Parse(request.Pattern, syntaxFlags)
		if err != nil {
			return nil, fmt.Errorf("the regular expression is invalid: %v", err)
		}
		content = &query.Regexp{
			Regexp:        parsed,
			CaseSensitive: request.CaseSensitive,
			Content:       true,
		}
	default:
		return nil, fmt.Errorf("unsupported mode %q: use %q or %q", request.Mode, ModeLiteral, ModeRegex)
	}

	parts := []query.Q{content}

	if len(request.PathGlobs) > 0 {
		globs := make([]query.Q, 0, len(request.PathGlobs))
		for _, glob := range request.PathGlobs {
			expression, err := globToRegexp(glob)
			if err != nil {
				return nil, err
			}
			parsed, err := syntax.Parse(expression, syntaxFlags)
			if err != nil {
				return nil, fmt.Errorf("the path glob %q cannot be compiled", glob)
			}
			globs = append(globs, &query.Regexp{Regexp: parsed, CaseSensitive: true, FileName: true})
		}
		parts = append(parts, query.NewOr(globs...))
	}

	if len(request.Languages) > 0 {
		languages := make([]query.Q, 0, len(request.Languages))
		for _, language := range request.Languages {
			if strings.TrimSpace(language) == "" {
				return nil, fmt.Errorf("a language filter is empty")
			}
			languages = append(languages, &query.Language{Language: canonicalLanguage(language)})
		}
		parts = append(parts, query.NewOr(languages...))
	}

	return query.NewAnd(parts...), nil
}

// globToRegexp translates a project-relative glob into an anchored expression.
//
// The translation refuses an absolute or escaping glob for the same reason
// [normalizeRelative] does: a filter is not a way to widen scope. Anchoring at
// both ends is deliberate, so `*.go` means a file at the project root and
// `**/*.go` means one at any depth, which is what a caller writing the glob
// expects.
func globToRegexp(glob string) (string, error) {
	trimmed := filepath.ToSlash(strings.TrimSpace(glob))
	switch {
	case trimmed == "":
		return "", fmt.Errorf("a path glob is empty")
	case strings.HasPrefix(trimmed, "/"):
		return "", fmt.Errorf("the path glob must be project-relative, not absolute: %q", glob)
	case trimmed == ".." || strings.HasPrefix(trimmed, "../"), strings.Contains(trimmed, "/../"):
		return "", fmt.Errorf("the path glob must stay inside the project: %q", glob)
	case looksAbsoluteWindows(trimmed):
		return "", fmt.Errorf("the path glob must be project-relative, not absolute: %q", glob)
	}

	var out strings.Builder
	out.WriteByte('^')
	for offset := 0; offset < len(trimmed); {
		switch trimmed[offset] {
		case '*':
			if offset+1 < len(trimmed) && trimmed[offset+1] == '*' {
				offset += 2
				if offset < len(trimmed) && trimmed[offset] == '/' {
					// `**/` matches any number of leading directories, including
					// none, so `**/*.go` also finds a file at the root.
					out.WriteString("(?:.*/)?")
					offset++
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
				offset++
			}
		case '?':
			out.WriteString("[^/]")
			offset++
		case '[':
			end := strings.IndexByte(trimmed[offset+1:], ']')
			if end < 0 {
				return "", fmt.Errorf("the path glob has an unterminated character class: %q", glob)
			}
			end += offset + 1
			class := trimmed[offset+1 : end]
			if class == "" {
				return "", fmt.Errorf("the path glob has an empty character class: %q", glob)
			}
			if class[0] == '!' {
				class = "^" + class[1:]
			}
			out.WriteByte('[')
			out.WriteString(class)
			out.WriteByte(']')
			offset = end + 1
		default:
			out.WriteString(quoteRegexpByte(trimmed[offset]))
			offset++
		}
	}
	out.WriteByte('$')
	return out.String(), nil
}

func quoteRegexpByte(value byte) string {
	if strings.ContainsRune(`\.+()|{}^$`, rune(value)) {
		return `\` + string(value)
	}
	return string(value)
}

// canonicalLanguage maps common spellings onto the names the backend uses. An
// unrecognized value is passed through rather than rejected, so a language the
// backend knows and this list does not still works.
func canonicalLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "c":
		return "C"
	case "c++", "cpp", "cxx":
		return "C++"
	case "c#", "csharp":
		return "C#"
	case "css":
		return "CSS"
	case "go", "golang":
		return "Go"
	case "html":
		return "HTML"
	case "java":
		return "Java"
	case "javascript", "js":
		return "JavaScript"
	case "json":
		return "JSON"
	case "jsx":
		return "JSX"
	case "kotlin", "kt":
		return "Kotlin"
	case "markdown", "md":
		return "Markdown"
	case "php":
		return "PHP"
	case "python", "py":
		return "Python"
	case "ruby", "rb":
		return "Ruby"
	case "rust", "rs":
		return "Rust"
	case "shell", "sh", "bash":
		return "Shell"
	case "sql":
		return "SQL"
	case "swift":
		return "Swift"
	case "toml":
		return "TOML"
	case "typescript", "ts":
		return "TypeScript"
	case "tsx":
		return "TSX"
	case "yaml", "yml":
		return "YAML"
	default:
		return strings.TrimSpace(language)
	}
}

// boundedPreview trims a long line around the match rather than from the start,
// so the returned text always contains what was matched.
func boundedPreview(line string, matchStart, matchEnd int) string {
	if len(line) <= maxPreviewBytes {
		return line
	}
	start := max(0, matchStart-maxPreviewBytes/2)
	end := min(len(line), max(start+maxPreviewBytes, matchEnd))
	start = max(0, end-maxPreviewBytes)
	// Nudge both ends onto rune boundaries so the preview is never invalid UTF-8.
	for start < matchStart && start < len(line) && !utf8.RuneStart(line[start]) {
		start++
	}
	for end > matchEnd && end > 0 && end < len(line) && !utf8.RuneStart(line[end]) {
		end--
	}
	return line[start:end]
}
