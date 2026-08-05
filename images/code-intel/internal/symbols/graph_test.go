package symbols

import (
	"context"
	"testing"
)

// TestFactsAcrossLanguages is the Stage 6 language boundary: every grammar that
// advertises outlines must also prove one declaration, dependency, and call. This
// prevents a new language from silently entering repository maps with no edges.
func TestFactsAcrossLanguages(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	tests := []struct {
		path, source, declaration, imported, caller, callee string
	}{
		{"main.go", "package x\nimport \"example/dep\"\nfunc Run(){ dep.Work() }\n", "Run", "example/dep", "Run", "Work"},
		{"main.py", "from app import dep\ndef run():\n    dep.work()\n", "run", "app", "run", "work"},
		{"lib.rs", "use crate::dep::Work;\nfn run(){ Work(); }\n", "run", "dep", "run", "Work"},
		{"main.c", "#include \"dep.h\"\nvoid run(){ work(); }\n", "run", "dep.h", "run", "work"},
		{"main.cpp", "#include \"dep.hpp\"\nvoid run(){ ns::work(); }\n", "run", "dep.hpp", "run", "work"},
		{"main.js", "import {work} from './dep.js';\nfunction run(){ work(); }\n", "run", "./dep.js", "run", "work"},
		{"main.ts", "import {work} from './dep';\nfunction run(): void { work(); }\n", "run", "./dep", "run", "work"},
		{"main.tsx", "import {Widget} from './widget';\nfunction View(){ return Widget(); }\n", "View", "./widget", "View", "Widget"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			facts, err := engine.Facts(context.Background(), test.path, []byte(test.source), "digest")
			if err != nil {
				t.Fatal(err)
			}
			if !hasDeclaration(facts, test.declaration) {
				t.Fatalf("declaration %q missing: %+v", test.declaration, facts.Declarations)
			}
			if !hasImport(facts, test.imported) {
				t.Fatalf("import %q missing: %+v", test.imported, facts.Imports)
			}
			if !hasCall(facts, test.caller, test.callee) {
				t.Fatalf("call %s -> %s missing: %+v", test.caller, test.callee, facts.Calls)
			}
		})
	}
}

func hasDeclaration(facts GraphFacts, name string) bool {
	for _, declaration := range facts.Declarations {
		if declaration.Name == name {
			return true
		}
	}
	return false
}

func hasImport(facts GraphFacts, target string) bool {
	for _, imported := range facts.Imports {
		if imported.Target == target {
			return true
		}
	}
	return false
}

func hasCall(facts GraphFacts, caller, callee string) bool {
	for _, call := range facts.Calls {
		if call.Caller == caller && call.Callee == callee {
			return true
		}
	}
	return false
}
