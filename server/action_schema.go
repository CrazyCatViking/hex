package hex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const MaxActionInputBytes = 1 << 20
const maxActionSchemaBytes = 64 << 10

// ActionDefinition is the discoverable contract for one app operation. Schemas
// default to JSON Schema draft 2020-12 and must be self-contained: local $refs
// and $defs are supported, but external resources are never fetched.
type ActionDefinition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

// ValidateSchemas checks both schemas before an operation is invoked.
func (d ActionDefinition) ValidateSchemas() error {
	if _, err := compileActionSchema(d.InputSchema); err != nil {
		return fmt.Errorf("invalid input schema: %w", err)
	}
	if _, err := compileActionSchema(d.OutputSchema); err != nil {
		return fmt.Errorf("invalid output schema: %w", err)
	}
	return nil
}

// ValidateInput checks JSON against the same schema used by the server.
func (d ActionDefinition) ValidateInput(input json.RawMessage) error {
	if len(input) > MaxActionInputBytes {
		return errors.New("action input exceeds 1 MiB")
	}
	schema, err := compileActionSchema(d.InputSchema)
	if err != nil {
		return fmt.Errorf("invalid input schema: %w", err)
	}
	return validateActionJSON(schema, input)
}

// ValidateOutput checks the response against the action's output contract.
func (d ActionDefinition) ValidateOutput(output json.RawMessage) error {
	schema, err := compileActionSchema(d.OutputSchema)
	if err != nil {
		return fmt.Errorf("invalid output schema: %w", err)
	}
	return validateActionJSON(schema, output)
}

type actionSchemaLoader struct{}

func (actionSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("action schemas must be self-contained; external schema references are disabled")
}

func compileActionSchema(data json.RawMessage) (*jsonschema.Schema, error) {
	if len(data) > maxActionSchemaBytes || !json.Valid(data) {
		return nil, errors.New("expected a JSON Schema of at most 64 KiB")
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(actionSchemaLoader{})
	compiler.AssertFormat()
	compiler.AssertVocabs()
	const location = "https://hex.invalid/action-schema"
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

func validateActionJSON(schema *jsonschema.Schema, data json.RawMessage) error {
	if !json.Valid(data) {
		return errors.New("expected exactly one JSON value")
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return schema.Validate(value)
}
