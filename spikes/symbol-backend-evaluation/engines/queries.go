package engines

// Symbol queries, one per language.
//
// Each pattern captures the whole declaration as @def and the name as @name. The
// kind comes from the @def node's own grammar type rather than from a third
// capture, so a query cannot claim a kind the syntax does not support.
//
// These are the patterns a production symbol layer would carry, which is the
// point of writing them here: the spike measures the queries that would ship, not
// a simplified stand-in that would make the candidate look better than it is.

const goQuery = `
(function_declaration name: (identifier) @name) @def
(method_declaration name: (field_identifier) @name) @def
(type_spec name: (type_identifier) @name) @def
(const_spec name: (identifier) @name) @def
(var_spec name: (identifier) @name) @def
(field_declaration name: (field_identifier) @name) @def
`

// goInterfaceMethodQueries are tried in order and the first that compiles is
// used. The node naming an interface's method changed between grammar releases,
// and a query referring to a node the loaded grammar does not have fails to
// compile rather than matching nothing, so the working name has to be discovered
// rather than assumed.
var goInterfaceMethodQueries = []string{
	`(method_elem name: (field_identifier) @name) @def`,
	`(method_spec name: (field_identifier) @name) @def`,
}

const pythonQuery = `
(function_definition name: (identifier) @name) @def
(class_definition name: (identifier) @name) @def
`

const rustQuery = `
(function_item name: (identifier) @name) @def
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
`

const cQuery = `
(function_definition declarator: (function_declarator declarator: (identifier) @name)) @def
(declaration declarator: (function_declarator declarator: (identifier) @name)) @def
(struct_specifier name: (type_identifier) @name) @def
(union_specifier name: (type_identifier) @name) @def
(enum_specifier name: (type_identifier) @name) @def
(type_definition declarator: (type_identifier) @name) @def
(enumerator name: (identifier) @name) @def
(field_declaration declarator: (field_identifier) @name) @def
`

const cppQuery = cQuery + `
(class_specifier name: (type_identifier) @name) @def
(namespace_definition name: (namespace_identifier) @name) @def
(function_definition declarator: (function_declarator declarator: (qualified_identifier) @name)) @def
(function_definition declarator: (function_declarator declarator: (field_identifier) @name)) @def
(field_declaration declarator: (function_declarator declarator: (field_identifier) @name)) @def
(alias_declaration name: (type_identifier) @name) @def
(concept_definition name: (identifier) @name) @def
`

// ecmaQuery is what JavaScript and TypeScript share.
//
// A class is deliberately not here: the two grammars disagree about the node that
// names one, and a query naming the wrong node does not silently match nothing —
// it fails to compile. That is the right failure, and it is why the shared part
// stops where the grammars diverge.
const ecmaQuery = `
(function_declaration name: (identifier) @name) @def
(generator_function_declaration name: (identifier) @name) @def
(method_definition name: (property_identifier) @name) @def
(variable_declarator name: (identifier) @name value: (arrow_function)) @def
(variable_declarator name: (identifier) @name value: (function_expression)) @def
`

const javascriptQuery = ecmaQuery + `
(class_declaration name: (identifier) @name) @def
`

const typescriptQuery = ecmaQuery + `
(class_declaration name: (type_identifier) @name) @def
(interface_declaration name: (type_identifier) @name) @def
(type_alias_declaration name: (type_identifier) @name) @def
(enum_declaration name: (identifier) @name) @def
(abstract_class_declaration name: (type_identifier) @name) @def
(function_signature name: (identifier) @name) @def
(method_signature name: (property_identifier) @name) @def
(public_field_definition name: (property_identifier) @name) @def
(internal_module name: (identifier) @name) @def
`

// kindByNode maps a grammar node type onto the normalized vocabulary.
//
// Every language's node names are in one table because the mapping is per node
// type and node types do not collide in practice; where they do, they mean the
// same thing.
var kindByNode = map[string]Kind{
	// Go
	"function_declaration": KindFunction,
	"method_declaration":   KindMethod,
	"type_spec":            KindType,
	"const_spec":           KindConstant,
	"var_spec":             KindVariable,
	"field_declaration":    KindField,
	"method_elem":          KindMethod,
	"method_spec":          KindMethod,
	// Python and JavaScript
	"function_definition":            KindFunction,
	"class_definition":               KindClass,
	"class_declaration":              KindClass,
	"generator_function_declaration": KindFunction,
	"method_definition":              KindMethod,
	"variable_declarator":            KindFunction,
	// TypeScript
	"interface_declaration":      KindInterface,
	"type_alias_declaration":     KindType,
	"enum_declaration":           KindEnum,
	"abstract_class_declaration": KindClass,
	"function_signature":         KindFunction,
	"method_signature":           KindMethod,
	"public_field_definition":    KindField,
	"internal_module":            KindModule,
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
	"declaration":          KindFunction,
	"struct_specifier":     KindStruct,
	"union_specifier":      KindStruct,
	"enum_specifier":       KindEnum,
	"type_definition":      KindType,
	"enumerator":           KindConstant,
	"class_specifier":      KindClass,
	"namespace_definition": KindModule,
	"alias_declaration":    KindType,
	"concept_definition":   KindType,
}
