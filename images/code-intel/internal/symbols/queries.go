package symbols

// Symbol queries, one per language, and identifier queries beside them.
//
// A symbol query captures the whole declaration as @def and its name as @name. The
// kind normally comes from the @def node's own grammar type, so a query cannot
// claim a kind the syntax does not support; where one node type is genuinely two
// things, the kind is named in the capture as @def.kind.
//
// @scope is a node that nests without declaring anything. A Rust `impl Config`
// block is the case it exists for: the methods inside it should report Config as
// their container, and the word Config there is a use of a type declared elsewhere.
//
// An identifier query captures every node the grammar calls an identifier. That is
// what makes an occurrence answer worth more than a text search: a name in a
// comment, in a string, or inside a longer word is not captured, because the
// grammar does not call any of those an identifier. Each set is written out rather
// than derived from a naming convention, so a node type renamed upstream fails to
// compile instead of silently going missing.

// --- Go ---

// goQuery covers what a Go file declares.
//
// Local declarations inside a function body match too, and that is deliberate: a
// `var`, `const`, or `:=` inside a function is a declaration a reader looks for,
// and the containment computed from byte ranges already says which function it is
// in.
const goQuery = `
(function_declaration name: (identifier) @name) @def
(method_declaration name: (field_identifier) @name) @def
(type_spec name: (type_identifier) @name) @def
(const_spec name: (identifier) @name) @def
(var_spec name: (identifier) @name) @def
(short_var_declaration left: (expression_list (identifier) @name)) @def
(field_declaration name: (field_identifier) @name) @def
`

// Not captured for Go, deliberately: parameters, receivers, and range bindings.
//
// They are declarations, and including them would put every parameter of every
// function into an outline, which is noise in the answer a reader actually asked
// for -- the declaration's own signature already shows them. A lookup for such a
// name reports zero declarations and its uses, each placed in the declaration that
// holds it, which is enough to find it.

// goInterfaceMethodPatterns are tried in order and the first that compiles is used.
//
// The node naming a method inside an interface was renamed between grammar
// releases. A query referring to a node the loaded grammar does not have fails to
// compile rather than matching nothing, so the working name has to be discovered at
// startup instead of pinned to whichever release was current when this was written.
var goInterfaceMethodPatterns = []string{
	`(method_elem name: (field_identifier) @name) @def`,
	`(method_spec name: (field_identifier) @name) @def`,
}

const goIdentifiers = `
(identifier) @id
(field_identifier) @id
(type_identifier) @id
(package_identifier) @id
(label_name) @id
`

// --- Python ---

// pythonQuery covers functions, classes, and the bindings that behave like
// declarations.
//
// Module-level and class-level assignment is here because Slice 4.1 measured its
// absence: `requests/docs/conf.py` is thirty-four names to a tag generator and was
// zero to a query that knew only about functions and classes. A configuration
// module is mostly assignment, and an outline that reported nothing about one would
// be useless.
const pythonQuery = `
(function_definition name: (identifier) @name) @def
(class_definition name: (identifier) @name) @def
(module (expression_statement (assignment left: (identifier) @name) @def.variable))
(class_definition
  body: (block (expression_statement (assignment left: (identifier) @name) @def.field)))
`

const pythonIdentifiers = `
(identifier) @id
`

// --- Rust ---

const rustQuery = `
(function_item name: (identifier) @name) @def
(function_signature_item name: (identifier) @name) @def.function
(struct_item name: (type_identifier) @name) @def
(enum_item name: (type_identifier) @name) @def
(union_item name: (type_identifier) @name) @def
(trait_item name: (type_identifier) @name) @def
(mod_item name: (identifier) @name) @def
(type_item name: (type_identifier) @name) @def
(const_item name: (identifier) @name) @def
(static_item name: (identifier) @name) @def
(macro_definition name: (identifier) @name) @def
(field_declaration name: (field_identifier) @name) @def
(enum_variant name: (identifier) @name) @def.constant
(let_declaration pattern: (identifier) @name) @def.variable
(impl_item type: (type_identifier) @name) @scope.implementation
(impl_item type: (generic_type type: (type_identifier) @name)) @scope.implementation
`

const rustIdentifiers = `
(identifier) @id
(type_identifier) @id
(field_identifier) @id
(primitive_type) @id
`

// --- C ---

// cQuery includes preprocessor definitions.
//
// Slice 4.1 measured why: `zlib/adler32.c` gives a tag generator BASE, DO1, DO2,
// DO4, and DO16 and gave a query without these patterns none of them. In C a macro
// is frequently the declaration a reader is looking for.
//
// A struct, union, enum, or class is matched only with its body. In C a mere
// mention of a type -- `struct Widget *w` in a parameter list -- is a
// struct_specifier carrying the name, and matching it would report a *use* as a
// declaration. That defect was found by running this against a real file rather
// than by reading the grammar. The cost is that a forward declaration is not
// reported, which is an omission where the other was a false claim.
const cQuery = `
(function_definition declarator: (function_declarator declarator: (identifier) @name)) @def
(declaration declarator: (function_declarator declarator: (identifier) @name)) @def.function
(struct_specifier name: (type_identifier) @name body: (field_declaration_list)) @def
(union_specifier name: (type_identifier) @name body: (field_declaration_list)) @def
(enum_specifier name: (type_identifier) @name body: (enumerator_list)) @def
(type_definition declarator: (type_identifier) @name) @def
(enumerator name: (identifier) @name) @def
(field_declaration declarator: (field_identifier) @name) @def
(preproc_def name: (identifier) @name) @def.macro
(preproc_function_def name: (identifier) @name) @def.macro
`

const cIdentifiers = `
(identifier) @id
(type_identifier) @id
(field_identifier) @id
`

// --- C++ ---

const cppQuery = cQuery + `
(class_specifier name: (type_identifier) @name body: (field_declaration_list)) @def
(namespace_definition name: (namespace_identifier) @name) @def
(function_definition declarator: (function_declarator declarator: (qualified_identifier) @name)) @def
(function_definition declarator: (function_declarator declarator: (field_identifier) @name)) @def.method
(field_declaration declarator: (function_declarator declarator: (field_identifier) @name)) @def.method
(alias_declaration name: (type_identifier) @name) @def
(concept_definition name: (identifier) @name) @def.type
`

const cppIdentifiers = cIdentifiers + `
(namespace_identifier) @id
`

// --- JavaScript and TypeScript ---

// ecmaQuery is what JavaScript and TypeScript share.
//
// A class is deliberately not here: the two grammars disagree about the node that
// names one, and a query naming the wrong node does not silently match nothing --
// it fails to compile. That is the right failure, and it is why the shared part
// stops where the grammars diverge.
//
// The two variable_declarator patterns are why capture names carry a kind. The node
// is the same whether the value is a function or a number, and only the pattern
// knows which.
// The two function patterns come before the general one because the first pattern
// that matches a declaration decides its kind. See capture.
const ecmaQuery = `
(function_declaration name: (identifier) @name) @def
(generator_function_declaration name: (identifier) @name) @def
(method_definition name: (property_identifier) @name) @def
(variable_declarator name: (identifier) @name value: (arrow_function)) @def.function
(variable_declarator name: (identifier) @name value: (function_expression)) @def.function
(variable_declarator name: (identifier) @name) @def.variable
`

const javascriptQuery = ecmaQuery + `
(class_declaration name: (identifier) @name) @def
(field_definition property: (property_identifier) @name) @def.field
`

const typescriptQuery = ecmaQuery + `
(class_declaration name: (type_identifier) @name) @def
(interface_declaration name: (type_identifier) @name) @def
(type_alias_declaration name: (type_identifier) @name) @def
(enum_declaration name: (identifier) @name) @def
(abstract_class_declaration name: (type_identifier) @name) @def.class
(function_signature name: (identifier) @name) @def.function
(method_signature name: (property_identifier) @name) @def.method
(public_field_definition name: (property_identifier) @name) @def.field
(property_signature name: (property_identifier) @name) @def.field
(internal_module name: (identifier) @name) @def.module
`

const ecmaIdentifiers = `
(identifier) @id
(property_identifier) @id
(shorthand_property_identifier) @id
`

const typescriptIdentifiers = ecmaIdentifiers + `
(type_identifier) @id
`

// kindByNode maps a grammar node type onto the normalized vocabulary.
//
// The mapping is per node type and node types do not collide across the grammars
// LCTK configures; where two grammars share a name they mean the same thing, which
// is why one table serves all of them. A node whose kind depends on the pattern
// rather than on the type is not here -- the query names it instead.
var kindByNode = map[string]Kind{
	// Go
	"function_declaration":  KindFunction,
	"method_declaration":    KindMethod,
	"type_spec":             KindType,
	"const_spec":            KindConstant,
	"var_spec":              KindVariable,
	"short_var_declaration": KindVariable,
	"field_declaration":     KindField,
	"method_elem":           KindMethod,
	"method_spec":           KindMethod,
	// Python, JavaScript, and TypeScript
	"function_definition":            KindFunction,
	"class_definition":               KindClass,
	"class_declaration":              KindClass,
	"generator_function_declaration": KindFunction,
	"method_definition":              KindMethod,
	"interface_declaration":          KindInterface,
	"type_alias_declaration":         KindType,
	"enum_declaration":               KindEnum,
	// Rust
	"function_item":    KindFunction,
	"struct_item":      KindStruct,
	"enum_item":        KindEnum,
	"union_item":       KindStruct,
	"trait_item":       KindInterface,
	"mod_item":         KindModule,
	"type_item":        KindType,
	"const_item":       KindConstant,
	"static_item":      KindVariable,
	"macro_definition": KindMacro,
	// C and C++
	"struct_specifier":     KindStruct,
	"union_specifier":      KindStruct,
	"enum_specifier":       KindEnum,
	"type_definition":      KindType,
	"enumerator":           KindConstant,
	"class_specifier":      KindClass,
	"namespace_definition": KindModule,
	"alias_declaration":    KindType,
}
