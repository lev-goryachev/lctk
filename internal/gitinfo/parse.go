package gitinfo

import (
	"strconv"
	"strings"
)

// parseStatus reads `git status --porcelain=v2 --branch -z`.
//
// Porcelain v2 is used rather than v1 because Git commits to its grammar for
// machine consumption, and -z because a path may contain a newline, a quote, or
// a backslash. In v1 those paths come back C-quoted and a parser has to unquote
// them, which is a second grammar to get wrong; with -z they arrive verbatim.
//
// The records:
//
//	# branch.oid <sha> | (initial)
//	# branch.head <branch> | (detached)
//	# branch.upstream <name>
//	# branch.ab +<ahead> -<behind>
//	1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
//	2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path> NUL <origPath>
//	u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
//	? <path>
//	! <path>
func parseStatus(raw []byte, maxFiles int) Status {
	status := Status{Repository: true}

	// Records are NUL-separated, and a rename record is followed by one extra
	// NUL-separated field, so the fields are walked with an index rather than
	// ranged over.
	fields := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")

	for i := 0; i < len(fields); i++ {
		record := fields[i]
		if record == "" {
			continue
		}

		switch {
		case strings.HasPrefix(record, "# "):
			parseHeader(&status, strings.TrimPrefix(record, "# "))

		case strings.HasPrefix(record, "1 "):
			change, ok := parseOrdinary(record)
			if ok {
				status.add(change, maxFiles)
			}

		case strings.HasPrefix(record, "2 "):
			change, ok := parseRename(record)
			// The original path is the next field, not part of this record.
			if i+1 < len(fields) {
				change.From = fields[i+1]
				i++
			}
			if ok {
				status.add(change, maxFiles)
			}

		case strings.HasPrefix(record, "u "):
			if path, ok := lastField(record, 11); ok {
				status.add(Change{Path: path, State: "conflicted", WorkingTree: true}, maxFiles)
			}

		case strings.HasPrefix(record, "? "):
			status.add(Change{
				Path: strings.TrimPrefix(record, "? "), State: "untracked", WorkingTree: true,
			}, maxFiles)

			// "!" records are ignored files. They are not changes, and they only
			// appear when asked for, which this package never does.
		}
	}

	status.Dirty = status.Total > 0
	if len(status.Commit) >= 7 {
		status.ShortCommit = status.Commit[:7]
	}
	return status
}

func parseHeader(status *Status, header string) {
	key, value, found := strings.Cut(header, " ")
	if !found {
		return
	}
	switch key {
	case "branch.oid":
		if value == "(initial)" {
			status.Unborn = true
			return
		}
		status.Commit = value
	case "branch.head":
		if value == "(detached)" {
			status.Detached = true
			return
		}
		status.Branch = value
	case "branch.upstream":
		status.Upstream = value
	case "branch.ab":
		ahead, behind, ok := strings.Cut(value, " ")
		if !ok {
			return
		}
		status.Ahead = signedCount(ahead)
		status.Behind = signedCount(behind)
	}
}

// signedCount reads "+3" or "-2". The sign carries the direction, which the
// field name already says, so only the magnitude is kept.
func signedCount(field string) int {
	value, err := strconv.Atoi(strings.TrimLeft(field, "+-"))
	if err != nil {
		return 0
	}
	return value
}

// parseOrdinary reads a "1" record: eight fixed fields, then the path.
func parseOrdinary(record string) (Change, bool) {
	parts := strings.SplitN(record, " ", 9)
	if len(parts) < 9 {
		return Change{}, false
	}
	change := changeFromXY(parts[1])
	change.Path = parts[8]
	return change, change.Path != ""
}

// parseRename reads a "2" record: nine fixed fields, then the path.
func parseRename(record string) (Change, bool) {
	parts := strings.SplitN(record, " ", 10)
	if len(parts) < 10 {
		return Change{}, false
	}
	change := changeFromXY(parts[1])
	change.Path = parts[9]
	// The rename or copy score field decides which it was; the XY code says "R"
	// or "C" in the same position, so the state is already correct.
	return change, change.Path != ""
}

// lastField returns the record's final space-separated field, given how many
// fixed fields precede it.
func lastField(record string, fixed int) (string, bool) {
	parts := strings.SplitN(record, " ", fixed+1)
	if len(parts) <= fixed {
		return "", false
	}
	return parts[fixed], parts[fixed] != ""
}

// changeFromXY reads the two-letter status code.
//
// X is the index, Y is the working tree. A path can be changed in both, and both
// facts are kept: a caller deciding whether to commit needs to know the working
// tree has moved on since the index was written.
func changeFromXY(xy string) Change {
	if len(xy) < 2 {
		return Change{}
	}
	index, worktree := xy[0], xy[1]

	change := Change{
		Staged:      index != '.',
		WorkingTree: worktree != '.',
	}
	// The index takes precedence when both changed, because it names the more
	// specific act: a file staged as a rename and then edited is still a rename.
	code := index
	if code == '.' {
		code = worktree
	}
	change.State = stateOf(code)
	return change
}

func stateOf(code byte) string {
	switch code {
	case 'A':
		return "added"
	case 'M':
		return "modified"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'T':
		return "type-changed"
	case 'U':
		return "conflicted"
	default:
		return "modified"
	}
}

// add records a change, counting every one but keeping only as many as asked.
//
// The count is the total rather than the kept length, so a caller that trims the
// list still learns how much it did not see.
func (s *Status) add(change Change, maxFiles int) {
	if change.Path == "" {
		return
	}
	s.Total++
	if len(s.Changed) >= maxFiles {
		s.Truncated = true
		return
	}
	s.Changed = append(s.Changed, change)
}
