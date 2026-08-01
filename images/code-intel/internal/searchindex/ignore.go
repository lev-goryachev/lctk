package searchindex

import (
	"bufio"
	"os"
	"path"
	"strings"
)

// Ignore file names, in the order their rules are applied within a directory.
// Later files win, because the last matching rule decides.
const (
	// GitIgnoreFile is consulted as a default, not as an authority.
	GitIgnoreFile = ".gitignore"
	// LctkIgnoreFile is the project's statement about indexing specifically. It
	// overrides the version-control file, including by re-including something
	// that file excluded.
	LctkIgnoreFile = ".lctkignore"
	// LctkIgnoreLocalFile is the same for one machine, and is expected to be
	// untracked. It mirrors the manifest's local override in
	// docs/security.md: a personal decision does not belong in a shared file.
	LctkIgnoreLocalFile = ".lctkignore.local"
)

var ignoreFileNames = []string{GitIgnoreFile, LctkIgnoreFile, LctkIgnoreLocalFile}

// Honouring ignore rules is a correctness requirement, not an optimization.
//
// ADR-0011 says enumeration comes from the filesystem rather than from Git
// objects, so that a file which is saved but never committed is still
// searchable. That is about content, not about scope: a directory the project
// has explicitly told its tooling to ignore is not part of the project, and
// indexing it wastes space, slows every query, and answers questions about
// build output and dependency caches instead of about the code.
//
// The rule was learned the hard way. Run against a real checkout, the first
// implementation walked an ignored local directory holding hundreds of
// thousands of cached files and never finished its first build.
//
// But a version-control ignore file answers "what should not be committed",
// which is a different question from "what should not be indexed". A local
// scratch directory is deliberately uncommitted and is exactly the sort of thing
// its owner wants to search. So .gitignore is a sensible default and a poor
// authority: .lctkignore is layered after it and can re-include anything, and
// .lctkignore.local does the same for one machine. A project that wants to
// disregard the version-control rules entirely starts its .lctkignore with a
// "!/**" line, which re-includes everything before its own rules narrow it
// again.

// pattern is one parsed ignore rule.
type pattern struct {
	// base is the directory the rule was declared in, relative to the workspace
	// root, using forward slashes and empty for the root itself.
	base string
	// glob is the rule with its markers stripped.
	glob string
	// negate reverses the outcome, as a leading "!" does in an ignore file.
	negate bool
	// dirOnly restricts the rule to directories, as a trailing "/" does.
	dirOnly bool
	// anchored restricts the rule to the directory it was declared in, which a
	// leading or embedded "/" does. An unanchored rule matches at any depth.
	anchored bool
}

// ignoreSet is the ordered set of rules in effect for one directory.
//
// Rules accumulate as the walk descends and are evaluated in order, with the
// last match deciding. That ordering is what makes a negation work: a nested
// file can re-include something a parent excluded.
type ignoreSet struct {
	patterns []pattern
}

// rootIgnoreSet is the starting set: LCTK's defaults, then the project's own
// root rules. Order matters, because the last match decides and the project must
// be able to overrule a default.
func rootIgnoreSet(root *os.Root, seen *sourceSet) ignoreSet {
	defaults := make([]pattern, 0, len(defaultIgnorePatterns))
	for _, line := range defaultIgnorePatterns {
		if parsed, ok := parsePattern(line, ""); ok {
			defaults = append(defaults, parsed)
		}
	}
	return ignoreSet{patterns: defaults}.withFiles(root, "", seen)
}

// withFiles returns a set extended by the ignore files in a directory.
//
// Each name is read in order, so a later file's rule beats an earlier one's. The
// directory is named relative to the workspace and read through the root, so an
// ignore file cannot be picked up from outside the project.
//
// The receiver is not modified, because sibling directories must not inherit one
// another's rules. Sharing the underlying array would do exactly that, so the
// slice is copied.
//
// Any file names it actually found are appended to seen, so the sources in
// effect can be reported rather than guessed at.
func (s ignoreSet) withFiles(root *os.Root, relativeBase string, seen *sourceSet) ignoreSet {
	var added []pattern
	for _, fileName := range ignoreFileNames {
		name := fileName
		if relativeBase != "" {
			name = path.Join(relativeBase, fileName)
		}
		found, ok := readPatterns(root, name, relativeBase)
		if !ok {
			continue
		}
		seen.add(fileName)
		added = append(added, found...)
	}
	if len(added) == 0 {
		return s
	}

	combined := make([]pattern, 0, len(s.patterns)+len(added))
	combined = append(combined, s.patterns...)
	combined = append(combined, added...)
	return ignoreSet{patterns: combined}
}

// readPatterns parses one ignore file, reporting whether it existed. An existing
// file with no usable rule still counts as present, because "the project has an
// empty .lctkignore" is a meaningful statement.
func readPatterns(root *os.Root, name, relativeBase string) ([]pattern, bool) {
	file, err := root.Open(name)
	if err != nil {
		return nil, false
	}
	defer file.Close()

	var parsed []pattern
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if rule, ok := parsePattern(scanner.Text(), relativeBase); ok {
			parsed = append(parsed, rule)
		}
	}
	return parsed, true
}

// isIgnoreFile reports whether a project-relative path names an ignore file.
func isIgnoreFile(relative string) bool {
	base := path.Base(path.Clean(strings.ReplaceAll(relative, `\`, "/")))
	for _, name := range ignoreFileNames {
		if base == name {
			return true
		}
	}
	return false
}

// touchesIgnoreRules reports whether a batch changes any ignore file.
func touchesIgnoreRules(changes []Change) bool {
	for _, change := range changes {
		if isIgnoreFile(change.Path) {
			return true
		}
	}
	return false
}

// sourceSet records which ignore files were found, in a stable order.
type sourceSet struct {
	names []string
}

func (s *sourceSet) add(name string) {
	if s == nil {
		return
	}
	for _, existing := range s.names {
		if existing == name {
			return
		}
	}
	s.names = append(s.names, name)
}

func (s *sourceSet) list() []string {
	if s == nil || len(s.names) == 0 {
		return nil
	}
	out := make([]string, 0, len(ignoreFileNames))
	// Reported in declaration order rather than discovery order, so the output
	// reads as the precedence chain it is.
	for _, candidate := range ignoreFileNames {
		for _, found := range s.names {
			if found == candidate {
				out = append(out, candidate)
				break
			}
		}
	}
	return out
}

// ignored reports whether a workspace-relative path is excluded.
func (s ignoreSet) ignored(relative string, isDir bool) bool {
	result := false
	for _, rule := range s.patterns {
		if rule.dirOnly && !isDir {
			continue
		}
		if rule.matches(relative) {
			result = !rule.negate
		}
	}
	return result
}

func (p pattern) matches(relative string) bool {
	subject := relative
	if p.base != "" {
		if !strings.HasPrefix(relative, p.base+"/") {
			return false
		}
		subject = relative[len(p.base)+1:]
	}

	if p.anchored {
		return matchGlob(p.glob, subject)
	}
	// An unanchored rule matches the entry itself or any ancestor segment, so
	// "node_modules" excludes the directory and everything beneath it.
	for {
		if matchGlob(p.glob, subject) {
			return true
		}
		slash := strings.IndexByte(subject, '/')
		if slash < 0 {
			return false
		}
		subject = subject[slash+1:]
	}
}

func parsePattern(line, base string) (pattern, bool) {
	trimmed := strings.TrimRight(line, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return pattern{}, false
	}

	rule := pattern{base: base}
	if strings.HasPrefix(trimmed, "!") {
		rule.negate = true
		trimmed = trimmed[1:]
	}
	// An escaped leading marker is a literal character, not a marker.
	trimmed = strings.TrimPrefix(trimmed, `\`)
	if trimmed == "" {
		return pattern{}, false
	}

	if strings.HasSuffix(trimmed, "/") {
		rule.dirOnly = true
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	if strings.HasPrefix(trimmed, "/") {
		rule.anchored = true
		trimmed = strings.TrimPrefix(trimmed, "/")
	} else if strings.Contains(trimmed, "/") {
		// A slash anywhere but at the end anchors the rule to its own directory.
		rule.anchored = true
	}
	if trimmed == "" {
		return pattern{}, false
	}

	rule.glob = trimmed
	return rule, true
}

// matchGlob matches one ignore glob against a slash-separated path.
//
// It is written here rather than taken from a library because the semantics
// wanted are narrow and specific: "*" and "?" stop at a separator, "**" crosses
// them, and a trailing "**" or a bare directory name matches everything beneath.
func matchGlob(glob, subject string) bool {
	if glob == "**" {
		return true
	}
	if matchSegments(splitGlob(glob), strings.Split(subject, "/")) {
		return true
	}
	// A rule naming a directory also covers its contents. Matching the leading
	// portion of the path expresses that without a separate directory pass.
	segments := splitGlob(glob)
	parts := strings.Split(subject, "/")
	if len(parts) > len(segments) {
		return matchSegments(segments, parts[:len(segments)])
	}
	return false
}

func splitGlob(glob string) []string { return strings.Split(glob, "/") }

// matchSegments matches path segments, treating "**" as any number of segments.
func matchSegments(pattern, parts []string) bool {
	switch {
	case len(pattern) == 0:
		return len(parts) == 0
	case pattern[0] == "**":
		for skip := 0; skip <= len(parts); skip++ {
			if matchSegments(pattern[1:], parts[skip:]) {
				return true
			}
		}
		return false
	case len(parts) == 0:
		return false
	case !matchSegment(pattern[0], parts[0]):
		return false
	default:
		return matchSegments(pattern[1:], parts[1:])
	}
}

// matchSegment matches one path segment against one glob segment.
func matchSegment(glob, name string) bool {
	// Iterative backtracking rather than recursion, so a pathological pattern
	// cannot blow the stack on a long file name.
	var (
		g, n     int
		starGlob = -1
		starName int
	)
	for n < len(name) {
		switch {
		case g < len(glob) && (glob[g] == '?' || glob[g] == name[n]):
			g++
			n++
		case g < len(glob) && glob[g] == '[':
			end := strings.IndexByte(glob[g+1:], ']')
			if end < 0 {
				return false
			}
			class := glob[g+1 : g+1+end]
			if !matchClass(class, name[n]) {
				if starGlob < 0 {
					return false
				}
				starName++
				g, n = starGlob+1, starName
				continue
			}
			g += end + 2
			n++
		case g < len(glob) && glob[g] == '*':
			starGlob = g
			starName = n
			g++
		case starGlob >= 0:
			starName++
			g, n = starGlob+1, starName
		default:
			return false
		}
	}
	for g < len(glob) && glob[g] == '*' {
		g++
	}
	return g == len(glob)
}

func matchClass(class string, char byte) bool {
	negate := false
	if strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^") {
		negate = true
		class = class[1:]
	}
	matched := false
	for i := 0; i < len(class); i++ {
		if i+2 < len(class) && class[i+1] == '-' {
			if char >= class[i] && char <= class[i+2] {
				matched = true
			}
			i += 2
			continue
		}
		if class[i] == char {
			matched = true
		}
	}
	return matched != negate
}
