package engines

import "testing"

// These check the harness itself, not the engine. A measurement is only evidence
// if the thing doing the measuring is right, and two of these properties -- exact
// containment and the syntax verdict -- are what the whole evaluation turns on.

func newEngine(t *testing.T) *TreeSitter {
	t.Helper()
	engine, err := NewTreeSitter()
	if err != nil {
		t.Fatalf("load the grammars: %v", err)
	}
	t.Cleanup(engine.Close)
	return engine
}

func analyse(t *testing.T, engine *TreeSitter, language, name, source string) FileResult {
	t.Helper()
	result := engine.Analyse(Request{Path: name, Language: language}, []byte(source))
	if result.Err != "" {
		t.Fatalf("analyse %s: %s", name, result.Err)
	}
	return result
}

func find(t *testing.T, result FileResult, name string) Symbol {
	t.Helper()
	for _, symbol := range result.Symbols {
		if symbol.Name == name {
			return symbol
		}
	}
	t.Fatalf("%q is not among the %d symbols found in %s", name, len(result.Symbols), result.Path)
	return Symbol{}
}

func TestEveryConfiguredLanguageCompilesItsOwnQuery(t *testing.T) {
	// A query that does not compile against the grammar actually loaded is a
	// configuration error, and it must surface here rather than as an empty answer
	// for one language in the middle of a corpus run.
	engine := newEngine(t)
	want := []string{"c", "cpp", "go", "javascript", "python", "rust", "tsx", "typescript"}
	got := engine.Languages()
	if len(got) != len(want) {
		t.Fatalf("languages = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("languages = %v, want %v", got, want)
			break
		}
	}
}

func TestContainmentComesFromTheTreeRatherThanFromANamePath(t *testing.T) {
	engine := newEngine(t)
	result := analyse(t, engine, "go", "a.go", `package p

type Outer struct {
	Field int
}

func (o Outer) Method() int { return o.Field }
`)

	if field := find(t, result, "Field"); field.Container != "Outer" {
		t.Errorf("Field is contained by %q, want Outer", field.Container)
	}
	method := find(t, result, "Method")
	if method.Container != "" {
		t.Errorf("Method is contained by %q; a method declaration is top-level in Go syntax", method.Container)
	}
	if method.Kind != KindMethod {
		t.Errorf("Method has kind %q", method.Kind)
	}
	if method.EndLine <= method.StartLine && method.EndByte <= method.StartByte {
		t.Errorf("Method has no extent: %+v", method)
	}
}

func TestASymbolIsBoundedInBytesAndTheBytesAreTheDeclaration(t *testing.T) {
	// The byte range is the gate Universal Ctags fails, so the harness has to show
	// it is a real extent rather than a plausible pair of numbers.
	engine := newEngine(t)
	source := `package p

func Add(a, b int) int {
	return a + b
}
`
	result := analyse(t, engine, "go", "a.go", source)
	add := find(t, result, "Add")
	extract := source[add.StartByte:add.EndByte]
	if extract[:len("func Add")] != "func Add" {
		t.Errorf("the byte range does not start at the declaration: %q", extract)
	}
	if extract[len(extract)-1] != '}' {
		t.Errorf("the byte range does not end at the declaration: %q", extract)
	}
}

func TestATruncatedFileIsReportedAsBrokenInEveryLanguageThatClaimsIt(t *testing.T) {
	engine := newEngine(t)
	for _, testCase := range brokenTestCases {
		t.Run(testCase.language, func(t *testing.T) {
			whole := analyse(t, engine, testCase.language, "whole", testCase.whole)
			if !whole.Parsed {
				t.Errorf("the whole file is reported as broken, with %d errors", whole.ParseErrors)
			}
			broken := analyse(t, engine, testCase.language, "broken", testCase.broken)
			if broken.Parsed {
				t.Error("the truncated file is reported as whole")
			}
			if broken.ParseErrors == 0 {
				t.Error("the truncated file is reported as broken but locates no error")
			}
		})
	}
}

// brokenTestCases mirrors the cases the `broken` command runs, kept here so the
// property is checked by `go test` and not only by reading a report.
var brokenTestCases = []struct {
	language string
	whole    string
	broken   string
}{
	{"go", "package p\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n", "package p\n\nfunc Add(a, b int) int {\n\treturn a +\n"},
	{"python", "class C:\n    def m(self):\n        return 1\n", "class C:\n    def m(self:\n        return 1\n"},
	{"rust", "pub struct S { pub f: u32 }\n", "pub struct S { pub f: u32\n"},
	{"javascript", "export function f(a) {\n  return a + 1;\n}\n", "export function f(a) {\n  return a +\n"},
	{"typescript", "export interface I { f: number }\n", "export interface I { f: number\n"},
}

func TestAnUnknownLanguageIsRefusedRatherThanGuessedAt(t *testing.T) {
	engine := newEngine(t)
	result := engine.Analyse(Request{Path: "a.rb", Language: "ruby"}, []byte("class C; end\n"))
	if result.Err == "" {
		t.Fatal("an unconfigured language produced an answer")
	}
	if len(result.Symbols) != 0 {
		t.Errorf("an unconfigured language produced %d symbols", len(result.Symbols))
	}
}

func TestAnExhaustedBudgetIsReportedAsUnanalysedRatherThanAsEmpty(t *testing.T) {
	// A budget of a single nanosecond cannot be met, which is the point: the
	// difference between "no symbols" and "not analysed" is the whole reason the
	// field exists.
	engine := newEngine(t)
	engine.Budget = 1
	result := engine.Analyse(Request{Path: "a.go", Language: "go"},
		[]byte("package p\n\nfunc Add(a, b int) int { return a + b }\n"))
	if !result.TimedOut {
		t.Skip("the parse finished inside a one-nanosecond budget, so the bound cannot be observed here")
	}
	if len(result.Symbols) != 0 {
		t.Errorf("an abandoned file reported %d symbols", len(result.Symbols))
	}
	if result.Parsed {
		t.Error("an abandoned file is reported as parsed")
	}
}
