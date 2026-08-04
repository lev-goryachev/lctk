package searchindex

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func filesContaining(t *testing.T, f *fixture, word string, maxFiles int) ([]string, bool) {
	t.Helper()
	paths, truncated, err := f.FilesContainingWord(context.Background(), word, maxFiles)
	if err != nil {
		t.Fatalf("FilesContainingWord(%q): %v", word, err)
	}
	return paths, truncated
}

func TestOnlyFilesHoldingTheWholeWordAreOffered(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "a.go", "package a\n\nfunc Needle() {}\n")
	f.write(t, "b.go", "package b\n\nfunc NeedleHolder() {}\n")
	f.write(t, "c.go", "package c\n\nfunc Other() {}\n")
	f.rebuild(t)

	paths, truncated := filesContaining(t, f, "Needle", 0)
	if truncated {
		t.Error("a three-file project reported truncation")
	}
	// b.go holds the letters inside a longer name and must not be offered: the
	// consumer parses every file it is given, so a false candidate is wasted work
	// and, if the parser were ever less careful, a wrong answer.
	if len(paths) != 1 || paths[0] != "a.go" {
		t.Errorf("candidates = %v, want only a.go", paths)
	}
}

// A candidate whose only hit is in a comment is still offered. That is correct and
// worth pinning: the index cannot tell prose from code, and deciding is the
// parser's job. What matters is that the file is not silently dropped here.
func TestAFileWhoseOnlyHitIsInACommentIsStillOffered(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "a.go", "package a\n\n// Needle is only mentioned here.\nfunc Other() {}\n")
	f.rebuild(t)

	paths, _ := filesContaining(t, f, "Needle", 0)
	if len(paths) != 1 || paths[0] != "a.go" {
		t.Errorf("candidates = %v, want a.go offered for the parser to judge", paths)
	}
}

func TestTheCandidateListIsBoundedAndSaysSo(t *testing.T) {
	f := newFixture(t, smallLimits)
	for index := 0; index < 12; index++ {
		f.write(t, "f"+strconv.Itoa(index)+".go", "package p\n\nvar Needle = 1\n")
	}
	f.rebuild(t)

	paths, truncated := filesContaining(t, f, "Needle", 5)
	if len(paths) != 5 {
		t.Errorf("candidates = %d, want the requested 5", len(paths))
	}
	// Without this flag a caller reading five files would conclude the name appears
	// in five files, which is the one wrong conclusion available here.
	if !truncated {
		t.Error("a cut list did not report truncation")
	}
}

func TestCandidatesComeBackInAStableOrder(t *testing.T) {
	f := newFixture(t, smallLimits)
	for _, name := range []string{"c.go", "a.go", "b.go"} {
		f.write(t, name, "package p\n\nvar Needle = 1\n")
	}
	f.rebuild(t)

	paths, _ := filesContaining(t, f, "Needle", 0)
	if strings.Join(paths, ",") != "a.go,b.go,c.go" {
		t.Errorf("candidates = %v, want sorted", paths)
	}
}

// The name reaches a regular expression, so anything that is not an identifier is
// refused rather than escaped. Escaping would silently accept a request that means
// something other than what the caller wrote.
func TestANameThatIsNotAnIdentifierIsRefused(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "a.go", "package a\n\nfunc Needle() {}\n")
	f.rebuild(t)

	for _, name := range []string{"", ".*", "Needle|Other", "Need(le", "a b", "Needle\\", strings.Repeat("x", 300)} {
		t.Run(name, func(t *testing.T) {
			_, _, err := f.FilesContainingWord(context.Background(), name, 0)
			if err == nil {
				t.Fatalf("FilesContainingWord(%q) was accepted", name)
			}
			var typed *Error
			if !errors.As(err, &typed) {
				t.Fatalf("error is not typed: %v", err)
			}
			if typed.Code != CodeInvalidPattern {
				t.Errorf("code = %q, want %q", typed.Code, CodeInvalidPattern)
			}
		})
	}
}

func TestAnIdentifierWithADigitOrUnderscoreIsAccepted(t *testing.T) {
	f := newFixture(t, smallLimits)
	f.write(t, "a.go", "package a\n\nvar sha256Sum, _private, v2 = 1, 2, 3\n")
	f.rebuild(t)

	for _, name := range []string{"sha256Sum", "_private", "v2"} {
		paths, _ := filesContaining(t, f, name, 0)
		if len(paths) != 1 {
			t.Errorf("%q offered %v", name, paths)
		}
	}
}
