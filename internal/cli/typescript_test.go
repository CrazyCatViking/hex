package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrefixItemsTypes(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
		want   string
	}{
		{"optional open prefix", `{"type":"array","prefixItems":[{"type":"string"},{"type":"number"}]}`, `[(string)?, (number)?, ...Array<unknown>]`},
		{"closed optional prefix", `{"type":"array","prefixItems":[{"type":"string"},{"type":"number"}],"items":false}`, `[(string)?, (number)?]`},
		{"required prefix and typed tail", `{"type":"array","prefixItems":[{"type":"string"},{"type":"number"}],"minItems":1,"items":{"type":"boolean"}}`, `[string, (number)?, ...Array<boolean>]`},
		{"minimum requires tail", `{"type":"array","prefixItems":[{"type":"string"}],"minItems":3,"items":{"type":"boolean"}}`, `[string, boolean, boolean, ...Array<boolean>]`},
		{"maximum truncates prefix", `{"type":"array","prefixItems":[{"type":"string"},{"type":"number"}],"maxItems":1}`, `[(string)?]`},
		{"maximum bounds tail", `{"type":"array","prefixItems":[{"type":"string"}],"minItems":1,"maxItems":3,"items":{"type":["boolean","null"]}}`, `[string, (boolean | null)?, (boolean | null)?]`},
		{"empty prefix", `{"type":"array","prefixItems":[],"items":{"type":"number"}}`, `[...Array<number>]`},
		{"zero maximum", `{"type":"array","prefixItems":[{"type":"string"}],"maxItems":0}`, `[]`},
		{"closed exact tuple", `{"type":"array","prefixItems":[{"type":"string"},{"type":"number"}],"minItems":2,"items":false}`, `[string, number]`},
		{"impossible closed minimum", `{"type":"array","prefixItems":[{"type":"string"}],"minItems":2,"items":false}`, `never`},
		{"impossible bounds", `{"type":"array","prefixItems":[],"minItems":2,"maxItems":1}`, `never`},
		{"large minimum is widened", `{"type":"array","prefixItems":[{"type":"string"}],"minItems":1000000,"items":{"type":"number"}}`, `Array<string | number>`},
		{"large maximum is widened", `{"type":"array","prefixItems":[{"type":"string"}],"maxItems":1000000,"items":{"type":"number"}}`, `[(string)?, ...Array<number>]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			types := newTypeScriptTypes()
			if _, err := types.declare("Tuple", "", json.RawMessage(test.schema)); err != nil {
				t.Fatal(err)
			}
			want := "export type Tuple = " + test.want + ";\n"
			if got := strings.Join(types.declarations, ""); got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

func TestCodegenEmptyInputUsesSchemaValidation(t *testing.T) {
	for _, test := range []struct {
		name     string
		schema   string
		optional bool
	}{
		{"true", `true`, true},
		{"false", `false`, false},
		{"optional object", `{"type":"object","properties":{"name":{"type":"string"}}}`, true},
		{"scalar", `{"type":"string"}`, false},
		{"array", `{"type":"array"}`, false},
		{"required", `{"type":"object","required":["name"]}`, false},
		{"ref required", `{"$ref":"#/$defs/input","$defs":{"input":{"type":"object","required":["name"]}}}`, false},
		{"ref optional", `{"$ref":"#/$defs/input","$defs":{"input":{"type":"object"}}}`, true},
		{"ref sibling", `{"$ref":"#/$defs/input","minProperties":1,"$defs":{"input":{"type":"object"}}}`, false},
		{"allOf", `{"allOf":[{"type":"object"},{"required":["name"]}]}`, false},
		{"anyOf required", `{"anyOf":[{"required":["a"]},{"required":["b"]}]}`, false},
		{"anyOf optional", `{"anyOf":[{"type":"string"},{"type":"object"}]}`, true},
		{"oneOf ambiguous", `{"oneOf":[{"type":"object"},{"type":"object"}]}`, false},
		{"not", `{"not":{"const":{}}}`, false},
		{"minProperties", `{"type":"object","minProperties":1}`, false},
		{"const", `{"const":{"name":"a"}}`, false},
		{"enum", `{"enum":[{},"a"]}`, true},
		{"conditional", `{"if":{"type":"object"},"then":{"required":["a"]}}`, false},
		{"unresolved reference", `{"$ref":"#/$defs/missing"}`, false},
		{"external reference", `{"$ref":"https://example.invalid/input"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := []catalogIntegration{{Name: "test", Endpoints: []catalogEndpoint{{
				Name: "run", InputSchema: json.RawMessage(test.schema), OutputSchema: json.RawMessage(`true`),
			}}}}
			module, err := generateIntegrationModule(catalog, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(module), "input: TestRunInput = {}"); got != test.optional {
				t.Fatalf("optional input = %v, want %v:\n%s", got, test.optional, module)
			}
		})
	}
}

func TestGeneratedTupleAssignments(t *testing.T) {
	compiler, err := exec.LookPath("tsc")
	if err != nil {
		t.Skip("TypeScript compiler is not installed")
	}
	types := newTypeScriptTypes()
	for name, schema := range map[string]string{
		"Open":    `{"type":"array","prefixItems":[{"type":"string"},{"type":"number"}]}`,
		"Closed":  `{"type":"array","prefixItems":[{"type":"string"},{"type":"number"}],"items":false}`,
		"Bounded": `{"type":"array","prefixItems":[{"type":"string"}],"minItems":1,"maxItems":3,"items":{"type":"boolean"}}`,
		"Minimum": `{"type":"array","prefixItems":[{"type":"string"}],"minItems":3,"items":{"type":"number"}}`,
	} {
		if _, err := types.declare(name, "", json.RawMessage(schema)); err != nil {
			t.Fatal(err)
		}
	}
	source := strings.Join(types.declarations, "\n") + `
const open: Open[] = [[], ["a"], ["a", 1], ["a", 1, null, {}]];
const closed: Closed[] = [[], ["a"], ["a", 1]];
const bounded: Bounded[] = [["a"], ["a", true], ["a", true, false]];
const minimum: Minimum[] = [["a", 1, 2], ["a", 1, 2, 3]];
// @ts-expect-error Prefix position retains its type.
const wrongPrefix: Open = [1];
// @ts-expect-error Closed tuples forbid a tail.
const wrongClosed: Closed = ["a", 1, true];
// @ts-expect-error maxItems bounds the tail.
const tooLong: Bounded = ["a", true, true, true];
// @ts-expect-error Typed tails retain their type.
const wrongTail: Bounded = ["a", 1];
// @ts-expect-error minItems requires the prefix.
const empty: Bounded = [];
// @ts-expect-error minItems can require tail positions.
const tooShort: Minimum = ["a", 1];
`
	path := filepath.Join(t.TempDir(), "tuples.ts")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(compiler, "--strict", "--noEmit", path).CombinedOutput(); err != nil {
		t.Fatalf("generated tuples do not typecheck: %v\n%s", err, output)
	}
}
