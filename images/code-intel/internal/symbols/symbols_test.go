package symbols

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func newEngine(t *testing.T) *Engine {
	t.Helper()
	engine, err := New()
	if err != nil {
		t.Fatalf("load the grammars: %v", err)
	}
	t.Cleanup(engine.Close)
	return engine
}

func outlineOf(t *testing.T, engine *Engine, name, source string) Outline {
	t.Helper()
	outline, err := engine.Outline(t.Context(), name, []byte(source), "digest")
	if err != nil {
		t.Fatalf("outline %s: %v", name, err)
	}
	return outline
}

func symbolNamed(t *testing.T, outline Outline, name string) Symbol {
	t.Helper()
	for _, symbol := range outline.Symbols {
		if symbol.Name == name {
			return symbol
		}
	}
	t.Fatalf("%q is not among the %d declarations found in %s", name, len(outline.Symbols), outline.Path)
	return Symbol{}
}

func typedError(t *testing.T, err error) *Error {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error is not typed: %v", err)
	}
	return typed
}

const goSource = `package p

// Doc comment, deliberately here: a symbol's extent must start at the
// declaration and not at whatever precedes it.
type Outer struct {
	Field int
	Other string
}

type Reader interface {
	Read(p []byte) (int, error)
}

const Limit = 10

func (o Outer) Method() int {
	const local = 2
	return o.Field * local
}

func Free(a, b int) int { return a + b }
`

func TestEveryDeclarationKindInAGoFileIsFound(t *testing.T) {
	outline := outlineOf(t, newEngine(t), "a.go", goSource)

	for name, want := range map[string]Kind{
		"Outer":  KindType,
		"Field":  KindField,
		"Other":  KindField,
		"Reader": KindType,
		"Read":   KindMethod,
		"Limit":  KindConstant,
		"Method": KindMethod,
		"local":  KindConstant,
		"Free":   KindFunction,
	} {
		if got := symbolNamed(t, outline, name).Kind; got != want {
			t.Errorf("%s has kind %q, want %q", name, got, want)
		}
	}
	if outline.Language != LanguageGo {
		t.Errorf("language = %q", outline.Language)
	}
	if outline.SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %d", outline.SchemaVersion)
	}
}

func TestContainmentIsTakenFromTheExtentsRatherThanFromANamePath(t *testing.T) {
	outline := outlineOf(t, newEngine(t), "a.go", goSource)

	if field := symbolNamed(t, outline, "Field"); field.Container != "Outer" || field.Depth != 1 {
		t.Errorf("Field is contained by %q at depth %d, want Outer at 1", field.Container, field.Depth)
	}
	// An interface's method is inside the type that declares it, which a name path
	// could not tell from a method declared beside it.
	if read := symbolNamed(t, outline, "Read"); read.Container != "Reader" {
		t.Errorf("Read is contained by %q, want Reader", read.Container)
	}
	// A constant declared inside a function body belongs to that function. This is
	// the case that makes containment worth computing: nothing in the constant's
	// own syntax says where it lives.
	if local := symbolNamed(t, outline, "local"); local.Container != "Method" {
		t.Errorf("local is contained by %q, want Method", local.Container)
	}
	// A method declaration is top-level in Go syntax whatever its receiver says.
	if method := symbolNamed(t, outline, "Method"); method.Container != "" || method.Depth != 0 {
		t.Errorf("Method is contained by %q at depth %d, want top level", method.Container, method.Depth)
	}
}

func TestTheByteRangeIsTheDeclarationAndExcludesWhatPrecedesIt(t *testing.T) {
	// The byte range is what makes "show me this declaration" answerable, so it has
	// to be a real extent rather than a plausible pair of numbers. A doc comment
	// sits above Outer on purpose: including it would silently shift every extent.
	outline := outlineOf(t, newEngine(t), "a.go", goSource)

	outer := symbolNamed(t, outline, "Outer")
	extract := goSource[outer.StartByte:outer.EndByte]
	if !strings.HasPrefix(extract, "Outer struct {") {
		t.Errorf("the extent starts at %q", first(extract, 40))
	}
	if strings.Contains(extract, "Doc comment") {
		t.Error("the extent swallowed the comment above the declaration")
	}
	if !strings.HasSuffix(extract, "}") {
		t.Errorf("the extent ends at %q", last(extract, 20))
	}

	free := symbolNamed(t, outline, "Free")
	if goSource[free.StartByte:free.EndByte] != "func Free(a, b int) int { return a + b }" {
		t.Errorf("Free's extent is %q", goSource[free.StartByte:free.EndByte])
	}
}

func TestTheSignatureIsTheDeclarationsOwnFirstLine(t *testing.T) {
	outline := outlineOf(t, newEngine(t), "a.go", goSource)

	if got := symbolNamed(t, outline, "Method").Signature; got != "func (o Outer) Method() int {" {
		t.Errorf("Method's signature is %q", got)
	}
	if got := symbolNamed(t, outline, "Outer").Signature; got != "Outer struct {" {
		t.Errorf("Outer's signature is %q", got)
	}
}

func TestALongOpeningLineIsCutOnARuneBoundary(t *testing.T) {
	// A generated or minified file can have one line of any length, and a caller
	// asked for an outline. Cutting mid-rune would put invalid UTF-8 into a JSON
	// response, which is a different failure from a long string.
	engine := newEngine(t)
	source := "package p\n\nconst Name = \"" + strings.Repeat("ф", 400) + "\"\n"
	outline := outlineOf(t, engine, "a.go", source)

	signature := symbolNamed(t, outline, "Name").Signature
	if len(signature) > maxSignatureBytes+8 {
		t.Errorf("the signature is %d bytes", len(signature))
	}
	if !strings.HasSuffix(signature, "…") {
		t.Errorf("a cut signature does not say it was cut: %q", last(signature, 12))
	}
	for _, r := range signature {
		if r == '�' {
			t.Fatal("the signature was cut mid-rune")
		}
	}
}

func TestAWholeFileParsesAndATruncatedOneDoesNot(t *testing.T) {
	engine := newEngine(t)

	whole := outlineOf(t, engine, "a.go", goSource)
	if !whole.Syntax.Reported || !whole.Syntax.Valid {
		t.Errorf("a valid Go file is reported as %+v", whole.Syntax)
	}
	if whole.Syntax.Errors != 0 {
		t.Errorf("a valid Go file has %d errors", whole.Syntax.Errors)
	}

	broken := outlineOf(t, engine, "a.go", "package p\n\nfunc Add(a, b int) int {\n\treturn a +\n")
	if broken.Syntax.Valid {
		t.Error("a truncated file is reported as valid")
	}
	if broken.Syntax.Errors == 0 {
		t.Error("a truncated file is reported as broken but locates no error")
	}
	if broken.Syntax.FirstErrorLine == 0 {
		t.Error("a truncated file does not say where to look")
	}
	// Whatever the file's state, the declarations that did parse are still worth
	// having: an agent asking for an outline of the file it is midway through
	// editing is the ordinary case, not an unusual one.
	if len(broken.Symbols) == 0 {
		t.Error("a truncated file yielded no declarations at all")
	}
}

func TestAnUnknownExtensionIsRefusedAndTheRefusalNamesWhatIsSupported(t *testing.T) {
	// An empty outline would read as "this file declares nothing", which is a
	// different and wrong claim. The refusal also has to say what would work, or a
	// caller learns the boundary only by guessing.
	engine := newEngine(t)
	_, err := engine.Outline(t.Context(), "a.rb", []byte("class C; end\n"), "digest")
	typed := typedError(t, err)

	if typed.Code != CodeUnsupportedLanguage {
		t.Errorf("code = %q, want %q", typed.Code, CodeUnsupportedLanguage)
	}
	if typed.Retryable {
		t.Error("an unsupported language is reported as retryable")
	}
	if !strings.Contains(typed.Message, LanguageGo) {
		t.Errorf("the refusal does not name what is supported: %q", typed.Message)
	}
}

func TestAFileWithNoDeclarationsAnswersWithAnEmptyListRatherThanNull(t *testing.T) {
	// A null reads to a model as "no answer" rather than "no declarations", which is
	// the same reason search never returns a nil match slice.
	outline := outlineOf(t, newEngine(t), "a.go", "package p\n")
	if outline.Symbols == nil {
		t.Fatal("symbols is nil")
	}
	if len(outline.Symbols) != 0 {
		t.Errorf("symbols = %+v", outline.Symbols)
	}
}

func TestLinesAndBytesDescribeTheFileThatWasRead(t *testing.T) {
	outline := outlineOf(t, newEngine(t), "a.go", "package p\n\nvar X = 1\n")
	if outline.Bytes != len("package p\n\nvar X = 1\n") {
		t.Errorf("bytes = %d", outline.Bytes)
	}
	// Three lines, not four: a trailing newline ends the last line.
	if outline.Lines != 3 {
		t.Errorf("lines = %d, want 3", outline.Lines)
	}
	if outline.Digest != "digest" {
		t.Errorf("digest = %q", outline.Digest)
	}
}

func TestAnExhaustedBudgetIsAnErrorRatherThanAnEmptyOutline(t *testing.T) {
	// "Not analysed" and "declares nothing" must not arrive as the same answer.
	engine := newEngine(t)
	engine.Budget = time.Nanosecond
	_, err := engine.Outline(context.Background(), "a.go", []byte(goSource), "digest")
	if err == nil {
		t.Skip("the parse finished inside a one-nanosecond budget, so the bound cannot be observed here")
	}
	typed := typedError(t, err)
	if typed.Code != CodeParseIncomplete {
		t.Errorf("code = %q, want %q", typed.Code, CodeParseIncomplete)
	}
	if typed.Retryable {
		t.Error("an exhausted budget is reported as retryable; the same file will exhaust it again")
	}
}

func TestConcurrentOutlinesDoNotShareAParser(t *testing.T) {
	// The HTTP surface serves concurrent requests and a Tree-sitter parser is not
	// safe for concurrent use, so the pool is load-bearing rather than an
	// optimization. This test is worth its cost only under -race.
	engine := newEngine(t)
	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for round := 0; round < 20; round++ {
				outline, err := engine.Outline(context.Background(), "a.go", []byte(goSource), "digest")
				if err != nil {
					t.Errorf("outline: %v", err)
					return
				}
				if len(outline.Symbols) == 0 {
					t.Error("a concurrent outline found no declarations")
					return
				}
			}
		}()
	}
	group.Wait()
}

func first(text string, count int) string {
	if len(text) <= count {
		return text
	}
	return text[:count]
}

func last(text string, count int) string {
	if len(text) <= count {
		return text
	}
	return text[len(text)-count:]
}

func TestExactlyTheBoundedNumberOfFilesIsParsedAtOnce(t *testing.T) {
	// Slice 4.5 measured why the bound exists: at 64 concurrent parses of a 920 KiB
	// C++ header on a two-CPU container, every single parse exceeded its five-second
	// budget and was refused, and resident memory reached 625 MiB. The bound is not
	// an optimization -- without it a busy service declines ordinary files.
	//
	// It is checked on the queue rather than by timing concurrent parses, which would
	// measure the machine rather than the rule.
	engine := newEngine(t)
	engine.SetParallelism(3)

	for taken := 1; taken <= 3; taken++ {
		if err := engine.acquire(context.Background()); err != nil {
			t.Fatalf("slot %d of 3 was refused: %v", taken, err)
		}
	}

	// The fourth must wait. A cancelled context is how a test observes "waits"
	// without depending on how long anything takes.
	full, cancel := context.WithCancel(context.Background())
	cancel()
	if err := engine.acquire(full); err == nil {
		t.Fatal("a fourth slot was granted against a bound of three")
	}

	engine.release()
	if err := engine.acquire(context.Background()); err != nil {
		t.Fatalf("a released slot was not reusable: %v", err)
	}
}

func TestACallerThatGaveUpDoesNotHoldAParsingSlot(t *testing.T) {
	// A slot held for an abandoned request is a slot spent on nothing, which is the
	// opposite of what a bound is for.
	engine := newEngine(t)
	engine.SetParallelism(1)

	// Fill the single slot and keep it.
	if err := engine.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := engine.Outline(ctx, "a.go", []byte("package p\n"), "")
	if err == nil {
		t.Fatal("a cancelled request was served from a full queue")
	}
	typed := typedError(t, err)
	if typed.Code != CodeParseBusy {
		t.Errorf("code = %q, want %q", typed.Code, CodeParseBusy)
	}
	// Retryable is the point of the distinction: the file is fine and the answer
	// exists, the project was busy. PARSE_INCOMPLETE would be a claim about the file.
	if !typed.Retryable {
		t.Error("a busy project is reported as not retryable")
	}

	engine.release()
	if _, err := engine.Outline(context.Background(), "a.go", []byte("package p\n"), ""); err != nil {
		t.Errorf("the slot was not returned: %v", err)
	}
}

func TestAnUnboundedEngineStillWorks(t *testing.T) {
	// Zero is right for a test and wrong for a service, and the difference has to be
	// a configuration rather than two code paths.
	engine := newEngine(t)
	engine.SetParallelism(0)
	if engine.Parallelism() != 0 {
		t.Fatalf("parallelism = %d, want unbounded", engine.Parallelism())
	}
	if _, err := engine.Outline(context.Background(), "a.go", []byte("package p\n"), ""); err != nil {
		t.Fatal(err)
	}
}
