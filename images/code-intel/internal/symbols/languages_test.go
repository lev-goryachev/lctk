package symbols

import (
	"strings"
	"testing"
)

// Each language gets one fixture holding the constructs an outline has to find, and
// every fixture mentions a sentinel name in a comment and in a string. Real code
// from each language's own project is measured separately and recorded in the
// roadmap; these pin the queries so a grammar upgrade that moves a node type fails
// here rather than in production.
type languageCase struct {
	language string
	path     string
	source   string
	// want maps a declaration name to the kind it must be reported as.
	want map[string]Kind
	// containers maps a declaration name to the declaration that must enclose it.
	containers map[string]string
	// sentinel appears as an identifier, in a comment, and in a string. The
	// occurrence count is how many of those are identifiers.
	sentinel            string
	sentinelOccurrences int
	// reportsSyntax says a verdict is published for this language.
	reportsSyntax bool
}

var languageCases = []languageCase{
	{
		language: LanguagePython,
		path:     "a.py",
		source: `# Widget is named in this comment.
NAME = "Widget in a string"

class Widget:
    size = 1

    def describe(self):
        return self.size

def build() -> Widget:
    return Widget()
`,
		want: map[string]Kind{
			"Widget": KindClass, "size": KindField, "describe": KindFunction,
			"build": KindFunction, "NAME": KindVariable,
		},
		containers:          map[string]string{"size": "Widget", "describe": "Widget"},
		sentinel:            "Widget",
		sentinelOccurrences: 3, // the class, the return annotation, and the call
		reportsSyntax:       true,
	},
	{
		language: LanguageRust,
		path:     "a.rs",
		source: `// Widget is named in this comment.
const NOTE: &str = "Widget in a string";

pub struct Widget {
    pub size: u32,
}

pub enum Shape {
    Round,
}

pub trait Describe {
    fn describe(&self) -> u32;
}

impl Widget {
    pub fn size_of(&self) -> u32 {
        let doubled = self.size * 2;
        doubled
    }
}
`,
		want: map[string]Kind{
			"Widget": KindStruct, "size": KindField, "Shape": KindEnum,
			"Round": KindConstant, "Describe": KindInterface, "describe": KindFunction,
			"NOTE": KindConstant, "size_of": KindFunction, "doubled": KindVariable,
		},
		// The impl block is a scope: methods inside it report the type as their
		// container, which is what a reader needs, without the block being a second
		// declaration of that type.
		containers: map[string]string{
			"size": "Widget", "Round": "Shape", "describe": "Describe",
			"size_of": "Widget", "doubled": "size_of",
		},
		sentinel:            "Widget",
		sentinelOccurrences: 2, // the struct and the impl target
		reportsSyntax:       true,
	},
	{
		language: LanguageC,
		path:     "a.c",
		source: `/* Widget is named in this comment. */
static const char *note = "Widget in a string";

#define WIDGET_MAX 10
#define WIDGET_TWICE(x) ((x) * 2)

struct Widget {
    int size;
};

enum Shape { ROUND };

typedef struct Widget WidgetAlias;

int widget_size(struct Widget *w) {
    return w->size;
}
`,
		want: map[string]Kind{
			"Widget": KindStruct, "size": KindField, "Shape": KindEnum,
			"ROUND": KindConstant, "WidgetAlias": KindType, "widget_size": KindFunction,
			"WIDGET_MAX": KindMacro, "WIDGET_TWICE": KindMacro,
		},
		containers:          map[string]string{"size": "Widget", "ROUND": "Shape"},
		sentinel:            "Widget",
		sentinelOccurrences: 3, // the struct, the typedef's target, and the parameter type
		reportsSyntax:       false,
	},
	{
		language: LanguageCPP,
		path:     "a.cpp",
		source: `// Widget is named in this comment.
namespace shapes {

class Widget {
public:
    int Size() const { return size_; }

private:
    int size_ = 0;
};

using WidgetAlias = Widget;

}  // namespace shapes
`,
		want: map[string]Kind{
			"shapes": KindModule, "Widget": KindClass, "Size": KindMethod,
			"WidgetAlias": KindType,
		},
		containers:          map[string]string{"Widget": "shapes", "Size": "Widget"},
		sentinel:            "Widget",
		sentinelOccurrences: 2, // the class and the alias target
		reportsSyntax:       false,
	},
	{
		language: LanguageJavaScript,
		path:     "a.js",
		source: `// Widget is named in this comment.
const note = "Widget in a string";

export class Widget {
  describe() {
    return 1;
  }
}

export const build = () => new Widget();

export function make() {
  return new Widget();
}
`,
		want: map[string]Kind{
			"Widget": KindClass, "describe": KindMethod,
			"build": KindFunction, "make": KindFunction, "note": KindVariable,
		},
		containers:          map[string]string{"describe": "Widget"},
		sentinel:            "Widget",
		sentinelOccurrences: 3, // the class and two constructions
		reportsSyntax:       true,
	},
	{
		language: LanguageTypeScript,
		path:     "a.ts",
		source: `// Widget is named in this comment.
const note = "Widget in a string";

export interface Widget {
  size: number;
}

export type WidgetAlias = Widget;

export enum Shape {
  Round,
}

export class Box implements Widget {
  size = 0;

  describe(): number {
    return this.size;
  }
}

export function build(w: Widget): number {
  return w.size;
}
`,
		want: map[string]Kind{
			"Widget": KindInterface, "size": KindField, "WidgetAlias": KindType,
			"Shape": KindEnum, "Box": KindClass, "describe": KindMethod,
			"build": KindFunction, "note": KindVariable,
		},
		containers:          map[string]string{"size": "Widget", "describe": "Box"},
		sentinel:            "Widget",
		sentinelOccurrences: 4, // the interface, the alias, the implements clause, the parameter
		reportsSyntax:       true,
	},
}

func TestEveryLanguageReportsItsOwnDeclarationsWithTheRightKinds(t *testing.T) {
	engine := newEngine(t)
	for _, testCase := range languageCases {
		t.Run(testCase.language, func(t *testing.T) {
			outline := outlineOf(t, engine, testCase.path, testCase.source)
			if outline.Language != testCase.language {
				t.Fatalf("language = %q, want %q", outline.Language, testCase.language)
			}
			for name, want := range testCase.want {
				got := symbolNamed(t, outline, name)
				if got.Kind != want {
					t.Errorf("%s has kind %q, want %q", name, got.Kind, want)
				}
				if got.EndByte <= got.StartByte {
					t.Errorf("%s has no extent: %+v", name, got)
				}
				if got.Signature == "" {
					t.Errorf("%s carries no signature", name)
				}
			}
			for name, container := range testCase.containers {
				if got := symbolNamed(t, outline, name).Container; got != container {
					t.Errorf("%s is contained by %q, want %q", name, got, container)
				}
			}
		})
	}
}

func TestEveryLanguageTellsAnIdentifierFromProse(t *testing.T) {
	// The property the whole occurrence answer rests on, checked once per grammar
	// because the identifier query is per grammar and a missing node type would
	// otherwise show up as a quietly smaller answer.
	engine := newEngine(t)
	for _, testCase := range languageCases {
		t.Run(testCase.language, func(t *testing.T) {
			located, err := engine.Locate(testCase.path, []byte(testCase.source), "digest", testCase.sentinel)
			if err != nil {
				t.Fatalf("Locate: %v", err)
			}
			if len(located.Occurrences) != testCase.sentinelOccurrences {
				var previews []string
				for _, occurrence := range located.Occurrences {
					previews = append(previews, occurrence.Preview)
				}
				t.Errorf("occurrences = %d, want %d: %v",
					len(located.Occurrences), testCase.sentinelOccurrences, previews)
			}
			for _, occurrence := range located.Occurrences {
				if strings.Contains(occurrence.Preview, "in this comment") {
					t.Errorf("a mention in a comment was reported: %+v", occurrence)
				}
				if strings.Contains(occurrence.Preview, "in a string") {
					t.Errorf("a mention in a string was reported: %+v", occurrence)
				}
			}
		})
	}
}

func TestEveryLanguageSaysWhetherItsSyntaxVerdictMeansAnything(t *testing.T) {
	// C and C++ withhold the verdict, and withholding it silently would leave a
	// caller reading `valid: false` as a defect in the project.
	engine := newEngine(t)
	for _, testCase := range languageCases {
		t.Run(testCase.language, func(t *testing.T) {
			outline := outlineOf(t, engine, testCase.path, testCase.source)
			if outline.Syntax.Reported != testCase.reportsSyntax {
				t.Fatalf("reported = %v, want %v", outline.Syntax.Reported, testCase.reportsSyntax)
			}
			if testCase.reportsSyntax {
				if !outline.Syntax.Valid {
					t.Errorf("valid source is reported broken: %+v", outline.Syntax)
				}
				if outline.Syntax.Note != "" {
					t.Errorf("a published verdict carries a note explaining its absence: %q", outline.Syntax.Note)
				}
				return
			}
			if outline.Syntax.Valid {
				t.Error("a withheld verdict reports valid: true, which reads as a claim")
			}
			if !strings.Contains(outline.Syntax.Note, "preprocessor") {
				t.Errorf("a withheld verdict does not say why: %q", outline.Syntax.Note)
			}
		})
	}
}

func TestARustImplBlockGivesContainmentWithoutDeclaringItsType(t *testing.T) {
	// The distinction a scope capture exists for. `impl Widget` must place its
	// methods under Widget and must not be reported as a second declaration of
	// Widget, or find_definition would answer "declared in two places".
	engine := newEngine(t)
	source := `pub struct Widget { pub size: u32 }

impl Widget {
    pub fn size_of(&self) -> u32 { self.size }
}
`
	located, err := engine.Locate("a.rs", []byte(source), "digest", "Widget")
	if err != nil {
		t.Fatal(err)
	}
	if located.Declarations != 1 {
		t.Errorf("declarations = %d, want 1: %+v", located.Declarations, located.Occurrences)
	}
	if len(located.Occurrences) != 2 {
		t.Fatalf("occurrences = %d, want 2", len(located.Occurrences))
	}

	outline := outlineOf(t, engine, "a.rs", source)
	if got := symbolNamed(t, outline, "size_of").Container; got != "Widget" {
		t.Errorf("size_of is contained by %q, want Widget", got)
	}
}

func TestAHeaderIsReadAsCAndSaysSo(t *testing.T) {
	// The extension genuinely does not say which language a .h is, and reading it as
	// C is what parses a C header correctly. The answer names the language it chose
	// rather than leaving a caller to guess.
	outline := outlineOf(t, newEngine(t), "a.h", "struct Widget { int size; };\n#define MAX 1\n")
	if outline.Language != LanguageC {
		t.Errorf("language = %q, want %q", outline.Language, LanguageC)
	}
	if got := symbolNamed(t, outline, "MAX").Kind; got != KindMacro {
		t.Errorf("MAX has kind %q", got)
	}
}

func TestTsxIsItsOwnGrammar(t *testing.T) {
	// TypeScript and TSX disagree about how `<T>` is read, so one grammar cannot
	// serve both. A .tsx file parsed as TypeScript reports errors on ordinary JSX.
	engine := newEngine(t)
	source := "export function View(): JSX.Element {\n  return <div>hello</div>;\n}\n"

	asTSX := outlineOf(t, engine, "a.tsx", source)
	if asTSX.Language != LanguageTSX {
		t.Fatalf("language = %q", asTSX.Language)
	}
	if !asTSX.Syntax.Valid {
		t.Errorf("JSX in a .tsx file is reported broken: %+v", asTSX.Syntax)
	}
	if got := symbolNamed(t, asTSX, "View").Kind; got != KindFunction {
		t.Errorf("View has kind %q", got)
	}
}

func TestEveryConfiguredLanguageIsAdvertised(t *testing.T) {
	engine := newEngine(t)
	want := []string{"c", "cpp", "go", "javascript", "python", "rust", "tsx", "typescript"}
	got := engine.Languages()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("languages = %v, want %v", got, want)
	}
}

// A mention of a struct type is not a declaration of it.
//
// In C, `struct Widget *w` in a parameter list is a struct_specifier carrying the
// name, so a query that did not require a body reported the parameter as a second
// declaration of Widget. Found by running against a real file, not by reading the
// grammar, which is why the test says what it is protecting against.
func TestMentioningACStructTypeIsNotDeclaringIt(t *testing.T) {
	engine := newEngine(t)
	source := "struct Widget { int size; };\n" +
		"int widget_size(struct Widget *w) { return w->size; }\n" +
		"struct Widget *widget_new(void);\n"

	outline := outlineOf(t, engine, "a.c", source)
	declarations := 0
	for _, symbol := range outline.Symbols {
		if symbol.Name == "Widget" {
			declarations++
		}
	}
	if declarations != 1 {
		var lines []int
		for _, symbol := range outline.Symbols {
			if symbol.Name == "Widget" {
				lines = append(lines, symbol.StartLine)
			}
		}
		t.Errorf("Widget is declared %d times, at lines %v, want once", declarations, lines)
	}

	located, err := engine.Locate("a.c", []byte(source), "digest", "Widget")
	if err != nil {
		t.Fatal(err)
	}
	if located.Declarations != 1 {
		t.Errorf("declarations = %d, want 1", located.Declarations)
	}
	// The mentions are still occurrences -- they are uses, and that is the answer a
	// caller wants about them.
	if len(located.Occurrences) != 3 {
		t.Errorf("occurrences = %d, want 3", len(located.Occurrences))
	}
}
