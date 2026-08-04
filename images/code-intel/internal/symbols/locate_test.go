package symbols

import (
	"strings"
	"testing"
)

// The file deliberately puts the same word in every place it must *not* count: a
// comment, a string literal, and inside a longer identifier. A text search finds
// all of them, and that is the difference this answer exists to make.
const locateSource = `package p

// Needle is mentioned in this comment and must not be an occurrence.
type Needle struct {
	Size int
}

const message = "Needle appears in this string literal too"

// NeedleHolder contains Needle as a substring of its own name.
type NeedleHolder struct {
	Held Needle
}

func Use(n Needle) int {
	other := Needle{Size: 1}
	return n.Size + other.Size
}
`

func locate(t *testing.T, engine *Engine, source, name string) Located {
	t.Helper()
	located, err := engine.Locate(t.Context(), "a.go", []byte(source), "digest", name)
	if err != nil {
		t.Fatalf("Locate(%q): %v", name, err)
	}
	return located
}

func TestAnOccurrenceIsAnIdentifierAndNotTheSameLettersInProse(t *testing.T) {
	located := locate(t, newEngine(t), locateSource, "Needle")

	for _, occurrence := range located.Occurrences {
		if strings.Contains(occurrence.Preview, "comment") {
			t.Errorf("a mention in a comment was reported: %+v", occurrence)
		}
		if strings.Contains(occurrence.Preview, "string literal") {
			t.Errorf("a mention in a string literal was reported: %+v", occurrence)
		}
	}
	// NeedleHolder contains the name and is not it. A word-boundary text search
	// would already exclude this one, but nothing about that is guaranteed once the
	// grammar decides what an identifier is, so it is checked here.
	for _, occurrence := range located.Occurrences {
		if occurrence.EndByte-occurrence.StartByte != len("Needle") {
			t.Errorf("an occurrence is not exactly the name: %+v", occurrence)
		}
	}
	// The declaration, the field type, the parameter type, and the composite
	// literal: four identifiers, and nothing from the comment or the string.
	if len(located.Occurrences) != 4 {
		var previews []string
		for _, occurrence := range located.Occurrences {
			previews = append(previews, occurrence.Preview)
		}
		t.Errorf("occurrences = %d: %v", len(located.Occurrences), previews)
	}
}

func TestTheDeclarationIsDistinguishedFromTheUses(t *testing.T) {
	located := locate(t, newEngine(t), locateSource, "Needle")

	if located.Declarations != 1 {
		t.Fatalf("declarations = %d, want 1", located.Declarations)
	}
	declarations := 0
	for _, occurrence := range located.Occurrences {
		if !occurrence.Declaration {
			continue
		}
		declarations++
		if occurrence.Kind != KindType {
			t.Errorf("the declaration has kind %q, want %q", occurrence.Kind, KindType)
		}
		// A type is not inside itself: the container of a declaration's own name is
		// whatever encloses the declaration, which here is nothing.
		if occurrence.Container != "" {
			t.Errorf("the declaration is contained by %q, want nothing", occurrence.Container)
		}
	}
	if declarations != 1 {
		t.Errorf("%d occurrences claim to be declarations", declarations)
	}
}

func TestAUseIsPlacedInTheInnermostDeclarationThatEnclosesIt(t *testing.T) {
	// This is what makes a reference list readable without opening every file. The
	// rule is the innermost declaration, the same rule an outline nests by, and it
	// is worth pinning because the answer is sometimes tighter than expected: a use
	// inside `other := Needle{...}` is inside the declaration of `other`, not
	// directly inside the function. Both are true and only one can be the innermost.
	located := locate(t, newEngine(t), locateSource, "Needle")

	containers := map[string]int{}
	for _, occurrence := range located.Occurrences {
		if occurrence.Declaration {
			continue
		}
		containers[occurrence.Container]++
	}
	if containers["Use"] != 1 {
		t.Errorf("the parameter type is placed in %v, want one directly under Use", containers)
	}
	if containers["other"] != 1 {
		t.Errorf("the composite literal is placed in %v, want one under the variable it initializes", containers)
	}
	if containers["Held"] != 1 {
		t.Errorf("the field's type is placed in %v, want one under Held", containers)
	}
	if containers[""] != 0 {
		t.Errorf("a use has no container at all: %v", containers)
	}
}

func TestAShortVariableDeclarationIsADeclaration(t *testing.T) {
	// `:=` is how most Go variables are declared, so a lookup that reported it as a
	// use would answer "nothing declares this" about most locals in the language.
	located := locate(t, newEngine(t), locateSource, "other")

	if located.Declarations != 1 {
		t.Fatalf("declarations = %d, want 1: %+v", located.Declarations, located.Occurrences)
	}
	for _, occurrence := range located.Occurrences {
		if !occurrence.Declaration {
			continue
		}
		if occurrence.Kind != KindVariable {
			t.Errorf("kind = %q, want %q", occurrence.Kind, KindVariable)
		}
		if occurrence.Container != "Use" {
			t.Errorf("the declaration is placed in %q, want Use", occurrence.Container)
		}
	}
}

func TestOccurrencesComeBackInFileOrder(t *testing.T) {
	located := locate(t, newEngine(t), locateSource, "Needle")
	for index := 1; index < len(located.Occurrences); index++ {
		if located.Occurrences[index].StartByte < located.Occurrences[index-1].StartByte {
			t.Fatalf("occurrence %d precedes %d in the file but follows it in the answer",
				index, index-1)
		}
	}
}

func TestANameNothingDeclaresYieldsNoOccurrencesRatherThanAnError(t *testing.T) {
	// An empty list is the right answer to "where is Absent used" in a file that
	// does not mention it, and a caller must be able to tell that from a failure.
	located := locate(t, newEngine(t), locateSource, "Absent")
	if located.Occurrences == nil {
		t.Fatal("occurrences is nil rather than empty")
	}
	if len(located.Occurrences) != 0 {
		t.Errorf("occurrences = %+v", located.Occurrences)
	}
}

func TestAFileThatDoesNotParseStillYieldsOccurrencesAndSaysSo(t *testing.T) {
	engine := newEngine(t)
	located := locate(t, engine, "package p\n\nfunc Use(n Needle) int {\n\treturn n.\n", "Needle")

	if len(located.Occurrences) == 0 {
		t.Error("a broken file yielded no occurrences at all")
	}
	if !located.SyntaxReported {
		t.Error("Go should publish a syntax verdict")
	}
	if located.Parsed {
		t.Error("a truncated file is reported as parsed")
	}
}

func TestALocalVariableIsFoundAndPlacedInItsFunction(t *testing.T) {
	source := `package p

func outer() int {
	total := 1
	return total
}

func other() int {
	total := 2
	return total
}
`
	located := locate(t, newEngine(t), source, "total")
	if len(located.Occurrences) != 4 {
		t.Fatalf("occurrences = %d, want 4", len(located.Occurrences))
	}
	// Two same-named locals in two functions. Nothing here resolves that they are
	// different variables, which is exactly what "name match" means -- and the
	// container is what lets a reader see it anyway.
	inOuter, inOther := 0, 0
	for _, occurrence := range located.Occurrences {
		switch occurrence.Container {
		case "outer":
			inOuter++
		case "other":
			inOther++
		default:
			t.Errorf("an occurrence is placed in %q", occurrence.Container)
		}
	}
	if inOuter != 2 || inOther != 2 {
		t.Errorf("outer holds %d and other %d", inOuter, inOther)
	}
}

func TestAnUnsupportedLanguageIsRefusedForALookupToo(t *testing.T) {
	engine := newEngine(t)
	_, err := engine.Locate(t.Context(), "a.rb", []byte("Needle = 1\n"), "digest", "Needle")
	typed := typedError(t, err)
	if typed.Code != CodeUnsupportedLanguage {
		t.Errorf("code = %q", typed.Code)
	}
}

func TestAPreviewIsTheSourceLineAndIsBounded(t *testing.T) {
	engine := newEngine(t)
	long := "package p\n\nvar Needle = \"" + strings.Repeat("x", maxPreviewBytes*2) + "\"\n"
	located := locate(t, engine, long, "Needle")
	if len(located.Occurrences) != 1 {
		t.Fatalf("occurrences = %d", len(located.Occurrences))
	}
	preview := located.Occurrences[0].Preview
	if len(preview) > maxPreviewBytes+8 {
		t.Errorf("the preview is %d bytes", len(preview))
	}
	if !strings.HasSuffix(preview, "…") {
		t.Errorf("a cut preview does not say it was cut: %q", preview[max(0, len(preview)-12):])
	}
}
