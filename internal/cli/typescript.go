package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// typeScriptTypes converts the JSON Schemas of integration endpoints into
// TypeScript declarations. It covers what endpoint contracts use: types
// (including unions such as ["number","null"]), objects with required and
// additional properties, arrays and tuples, enum and const, anyOf, oneOf,
// allOf, and local references to $defs or definitions, which become named
// types so recursive schemas work. Anything else is typed as unknown.
type typeScriptTypes struct {
	declarations []string
	declared     map[string]bool
}

// schemaScope resolves local references within one endpoint schema.
type schemaScope struct {
	root    map[string]any
	prefix  string
	pending map[string]bool
}

var (
	typeScriptIdentifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	nameSeparators       = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

const maxSchemaDepth = 64

func newTypeScriptTypes() *typeScriptTypes {
	return &typeScriptTypes{declared: make(map[string]bool)}
}

// declare adds a named declaration for a schema and returns whether the
// schema allows an empty object, so callers can make the argument optional.
func (t *typeScriptTypes) declare(name, description string, schema json.RawMessage) (bool, error) {
	var decoded any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return false, fmt.Errorf("decode schema for %s: %w", name, err)
	}
	root, _ := decoded.(map[string]any)
	scope := &schemaScope{root: root, prefix: name, pending: make(map[string]bool)}

	body := t.typeOf(decoded, scope, 0, 0)
	t.addDeclaration(name, description, decoded, body)
	return acceptsEmptyObject(decoded), nil
}

func (t *typeScriptTypes) addDeclaration(name, description string, schema any, body string) {
	if t.declared[name] {
		return
	}
	t.declared[name] = true
	var builder strings.Builder
	builder.WriteString(docComment(description, 0))
	if object, isObject := schema.(map[string]any); isObject && isPlainObject(object) && strings.HasPrefix(body, "{") {
		builder.WriteString("export interface " + name + " " + body + "\n")
	} else {
		builder.WriteString("export type " + name + " = " + body + ";\n")
	}
	t.declarations = append(t.declarations, builder.String())
}

func (t *typeScriptTypes) typeOf(schema any, scope *schemaScope, indent, depth int) string {
	if depth > maxSchemaDepth {
		return "unknown"
	}
	switch value := schema.(type) {
	case bool:
		if value {
			return "unknown"
		}
		return "never"
	case map[string]any:
		return t.objectSchemaType(value, scope, indent, depth)
	default:
		return "unknown"
	}
}

func (t *typeScriptTypes) objectSchemaType(schema map[string]any, scope *schemaScope, indent, depth int) string {
	if reference, isReference := schema["$ref"].(string); isReference {
		return t.reference(reference, scope)
	}
	if constant, hasConstant := schema["const"]; hasConstant {
		return literal(constant)
	}
	if values, isEnum := schema["enum"].([]any); isEnum && len(values) > 0 {
		literals := make([]string, 0, len(values))
		for _, value := range values {
			literals = append(literals, literal(value))
		}
		return strings.Join(literals, " | ")
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if options, present := schema[keyword].([]any); present && len(options) > 0 {
			return t.combine(options, " | ", scope, indent, depth)
		}
	}
	if parts, present := schema["allOf"].([]any); present && len(parts) > 0 {
		return t.combine(parts, " & ", scope, indent, depth)
	}

	types := schemaTypes(schema)
	if len(types) == 0 {
		switch {
		case schema["properties"] != nil || schema["additionalProperties"] != nil:
			types = []string{"object"}
		case schema["items"] != nil || schema["prefixItems"] != nil:
			types = []string{"array"}
		default:
			return "unknown"
		}
	}
	rendered := make([]string, 0, len(types))
	for _, kind := range types {
		rendered = append(rendered, t.typeForKind(kind, schema, scope, indent, depth))
	}
	return strings.Join(rendered, " | ")
}

func (t *typeScriptTypes) combine(options []any, separator string, scope *schemaScope, indent, depth int) string {
	rendered := make([]string, 0, len(options))
	for _, option := range options {
		text := t.typeOf(option, scope, indent, depth+1)
		if strings.ContainsAny(text, "|&") && !strings.HasPrefix(text, "{") {
			text = "(" + text + ")"
		}
		if !slices.Contains(rendered, text) {
			rendered = append(rendered, text)
		}
	}
	return strings.Join(rendered, separator)
}

func (t *typeScriptTypes) typeForKind(kind string, schema map[string]any, scope *schemaScope, indent, depth int) string {
	switch kind {
	case "string":
		return "string"
	case "number", "integer":
		return "number"
	case "boolean":
		return "boolean"
	case "null":
		return "null"
	case "array":
		return t.arrayType(schema, scope, indent, depth)
	case "object":
		return t.objectType(schema, scope, indent, depth)
	default:
		return "unknown"
	}
}

func (t *typeScriptTypes) arrayType(schema map[string]any, scope *schemaScope, indent, depth int) string {
	if prefix, isTuple := schema["prefixItems"].([]any); isTuple {
		elements := make([]string, 0, len(prefix))
		for _, item := range prefix {
			elements = append(elements, t.typeOf(item, scope, indent, depth+1))
		}
		return "[" + strings.Join(elements, ", ") + "]"
	}
	items, hasItems := schema["items"]
	if !hasItems {
		return "unknown[]"
	}
	element := t.typeOf(items, scope, indent, depth+1)
	if strings.ContainsAny(element, "|&") && !strings.HasPrefix(element, "{") {
		return "Array<" + element + ">"
	}
	return element + "[]"
}

func (t *typeScriptTypes) objectType(schema map[string]any, scope *schemaScope, indent, depth int) string {
	properties, _ := schema["properties"].(map[string]any)
	additional, hasAdditional := schema["additionalProperties"]
	if len(properties) == 0 {
		if hasAdditional && additional == false {
			return "Record<string, never>"
		}
		if hasAdditional && additional != true {
			return "Record<string, " + t.typeOf(additional, scope, indent, depth+1) + ">"
		}
		return "Record<string, unknown>"
	}

	required := stringSet(schema["required"])
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	slices.Sort(names)

	inner := strings.Repeat("  ", indent+1)
	var builder strings.Builder
	builder.WriteString("{\n")
	for _, name := range names {
		property := properties[name]
		if description := schemaDescription(property); description != "" {
			builder.WriteString(docComment(description, indent+1))
		}
		optional := "?"
		if required[name] {
			optional = ""
		}
		builder.WriteString(inner + propertyName(name) + optional + ": " + t.typeOf(property, scope, indent+1, depth+1) + ";\n")
	}
	// Extra properties are typed only when the schema describes them;
	// otherwise the declared properties are what an app may rely on.
	if hasAdditional && additional != false && additional != true {
		builder.WriteString(inner + "[key: string]: " + t.typeOf(additional, scope, indent+1, depth+1) + ";\n")
	}
	builder.WriteString(strings.Repeat("  ", indent) + "}")
	return builder.String()
}

// reference names a local definition, declaring it the first time; a
// reference that is being declared refers to itself, which TypeScript
// allows for named types.
func (t *typeScriptTypes) reference(reference string, scope *schemaScope) string {
	var key string
	switch {
	case strings.HasPrefix(reference, "#/$defs/"):
		key = "$defs"
	case strings.HasPrefix(reference, "#/definitions/"):
		key = "definitions"
	default:
		return "unknown"
	}
	definitionName := strings.TrimPrefix(strings.TrimPrefix(reference, "#/$defs/"), "#/definitions/")
	definitions, _ := scope.root[key].(map[string]any)
	definition, found := definitions[definitionName]
	if !found {
		return "unknown"
	}

	name := scope.prefix + pascalCase(definitionName)
	if t.declared[name] || scope.pending[name] {
		return name
	}
	scope.pending[name] = true
	body := t.typeOf(definition, scope, 0, 0)
	delete(scope.pending, name)
	t.addDeclaration(name, schemaDescription(definition), definition, body)
	return name
}

func schemaTypes(schema map[string]any) []string {
	switch value := schema["type"].(type) {
	case string:
		return []string{value}
	case []any:
		types := make([]string, 0, len(value))
		for _, item := range value {
			if kind, isString := item.(string); isString {
				types = append(types, kind)
			}
		}
		return types
	default:
		return nil
	}
}

// isPlainObject reports whether a schema is exactly an object type, which
// can be declared as an interface.
func isPlainObject(schema map[string]any) bool {
	types := schemaTypes(schema)
	isObject := len(types) == 1 && types[0] == "object" || len(types) == 0 && schema["properties"] != nil
	_, isReference := schema["$ref"]
	_, hasEnum := schema["enum"]
	return isObject && !isReference && !hasEnum && schema["anyOf"] == nil && schema["oneOf"] == nil && schema["allOf"] == nil
}

func acceptsEmptyObject(schema any) bool {
	object, isObject := schema.(map[string]any)
	if !isObject {
		return schema == true
	}
	return len(stringSet(object["required"])) == 0
}

func stringSet(value any) map[string]bool {
	set := make(map[string]bool)
	items, _ := value.([]any)
	for _, item := range items {
		if text, isString := item.(string); isString {
			set[text] = true
		}
	}
	return set
}

func schemaDescription(schema any) string {
	object, _ := schema.(map[string]any)
	description, _ := object["description"].(string)
	return description
}

func literal(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "unknown"
	}
	switch value.(type) {
	case string, float64, bool, nil:
		return string(encoded)
	default:
		return "unknown"
	}
}

func propertyName(name string) string {
	if typeScriptIdentifier.MatchString(name) {
		return name
	}
	encoded, _ := json.Marshal(name)
	return string(encoded)
}

func docComment(text string, indent int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	prefix := strings.Repeat("  ", indent)
	text = strings.ReplaceAll(text, "*/", "*\\/")
	lines := strings.Split(text, "\n")
	if len(lines) == 1 {
		return prefix + "/** " + lines[0] + " */\n"
	}
	var builder strings.Builder
	builder.WriteString(prefix + "/**\n")
	for _, line := range lines {
		builder.WriteString(strings.TrimRight(prefix+" * "+line, " ") + "\n")
	}
	builder.WriteString(prefix + " */\n")
	return builder.String()
}

// pascalCase turns names such as pull-requests into PullRequests.
func pascalCase(name string) string {
	var builder strings.Builder
	for _, part := range nameSeparators.Split(name, -1) {
		if part == "" {
			continue
		}
		builder.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return builder.String()
}

// camelCase turns names such as pull-requests into pullRequests.
func camelCase(name string) string {
	pascal := pascalCase(name)
	if pascal == "" {
		return pascal
	}
	return strings.ToLower(pascal[:1]) + pascal[1:]
}
