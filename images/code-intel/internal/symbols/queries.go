package symbols

// Symbol queries, one per language.
//
// Each pattern captures the whole declaration as @def and its name as @name. The
// kind comes from the @def node's own grammar type rather than from a third
// capture, so a query cannot claim a kind the syntax does not support.

// goQuery covers what a Go file declares.
//
// Local declarations inside a function body match too, and that is deliberate:
// `var` and `const` inside a function are declarations a reader looks for, and the
// containment computed from byte ranges already says which function they are in.
const goQuery = `
(function_declaration name: (identifier) @name) @def
(method_declaration name: (field_identifier) @name) @def
(type_spec name: (type_identifier) @name) @def
(const_spec name: (identifier) @name) @def
(var_spec name: (identifier) @name) @def
(short_var_declaration left: (expression_list (identifier) @name)) @def
(field_declaration name: (field_identifier) @name) @def
`

// Not captured, deliberately: parameters, receivers, and range bindings.
//
// They are declarations, and including them would put every parameter of every
// function into an outline, which is noise in the answer a reader actually asked
// for -- the declaration's own signature already shows them. A lookup for such a
// name reports zero declarations and its uses, each placed in the function that
// declares it, which is enough to find it.

// goInterfaceMethodPatterns are tried in order and the first that compiles is
// used.
//
// The node naming a method inside an interface was renamed between grammar
// releases. A query referring to a node the loaded grammar does not have fails to
// compile rather than matching nothing, so the working name has to be discovered
// at startup instead of pinned to whichever release was current when this was
// written.
var goInterfaceMethodPatterns = []string{
	`(method_elem name: (field_identifier) @name) @def`,
	`(method_spec name: (field_identifier) @name) @def`,
}

// goIdentifiers captures every node that is an identifier in Go.
//
// This is what makes an occurrence answer worth more than a text search: a name
// inside a comment, inside a string literal, or as part of a longer word is not
// captured, because the grammar does not call any of those an identifier. The set
// is written out rather than derived from a naming convention, so a node type
// added or renamed upstream is a compile error rather than a silent omission.
const goIdentifiers = `
(identifier) @id
(field_identifier) @id
(type_identifier) @id
(package_identifier) @id
(label_name) @id
`

// kindByNode maps a grammar node type onto the normalized vocabulary.
//
// The mapping is per node type and node types do not collide across the grammars
// LCTK configures; where two grammars share a name they mean the same thing, which
// is why one table serves all of them.
var kindByNode = map[string]Kind{
	"function_declaration":  KindFunction,
	"method_declaration":    KindMethod,
	"type_spec":             KindType,
	"const_spec":            KindConstant,
	"var_spec":              KindVariable,
	"short_var_declaration": KindVariable,
	"field_declaration":     KindField,
	"method_elem":           KindMethod,
	"method_spec":           KindMethod,
}
