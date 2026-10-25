package hex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
)

// RecordCondition is a bounded expression tree, shared by the in-memory
// evaluator and PostgreSQL query compiler. A nil condition matches everything.
// Data fields start with data.; createdBy and id refer to server metadata.
type RecordCondition struct {
	All   []RecordCondition `json:"all,omitempty"`
	Any   []RecordCondition `json:"any,omitempty"`
	Field string            `json:"field,omitempty"`
	Op    string            `json:"op,omitempty"`
	Value json.RawMessage   `json:"value,omitempty"`
}

func validConditionField(field string) bool {
	if field == "createdBy" || field == "id" {
		return true
	}
	if !strings.HasPrefix(field, "data.") || len(field) > 256 {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(field, "data."), ".") {
		if !validCollectionName(part) {
			return false
		}
	}
	return true
}

func validateRecordCondition(condition *RecordCondition) error {
	nodes := 0
	var walk func(*RecordCondition, int) error
	walk = func(node *RecordCondition, depth int) error {
		if node == nil {
			return nil
		}
		nodes++
		if depth > 8 || nodes > 128 {
			return errors.New("record conditions allow at most 8 levels and 128 nodes")
		}
		if node.All != nil || node.Any != nil {
			if node.All != nil && node.Any != nil || node.Field != "" || node.Op != "" || len(node.Value) > 0 || len(node.All)+len(node.Any) == 0 {
				return errors.New("use a nonempty all or any condition, or a field comparison")
			}
			for _, child := range append(slices.Clone(node.All), node.Any...) {
				if err := walk(&child, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		if !validConditionField(node.Field) || !slices.Contains([]string{"eq", "ne", "in", "contains"}, node.Op) || !json.Valid(node.Value) {
			return errors.New("a record comparison needs a valid field, eq/ne/in/contains operation and JSON value")
		}
		if len(node.Value) > 4096 {
			return errors.New("condition values are limited to 4 KiB")
		}
		if node.Op == "in" {
			var values []json.RawMessage
			if err := json.Unmarshal(node.Value, &values); err != nil || values == nil || len(values) > 128 {
				return errors.New("in needs an array of at most 128 values")
			}
		}
		return nil
	}
	return walk(condition, 0)
}

func decodeConditionJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func equalConditionValue(left, right any) bool {
	if number, ok := left.(json.Number); ok {
		other, ok := right.(json.Number)
		if !ok {
			return false
		}
		a, scaleA := normalizedConditionNumber(number)
		b, scaleB := normalizedConditionNumber(other)
		return a == b && scaleA.Cmp(scaleB) == 0
	}
	if object, ok := left.(map[string]any); ok {
		other, ok := right.(map[string]any)
		if !ok || len(object) != len(other) {
			return false
		}
		for key, value := range object {
			otherValue, exists := other[key]
			if !exists || !equalConditionValue(value, otherValue) {
				return false
			}
		}
		return true
	}
	if array, ok := left.([]any); ok {
		other, ok := right.([]any)
		if !ok || len(array) != len(other) {
			return false
		}
		for i := range array {
			if !equalConditionValue(array[i], other[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(left, right)
}

// Normalize decimal notation without constructing 10^exponent. A valid JSON
// number can contain an enormous exponent even in a tiny request body.
func normalizedConditionNumber(number json.Number) (string, *big.Int) {
	text := string(number)
	scale := new(big.Int)
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		scale.SetString(text[index+1:], 10)
		text = text[:index]
	}
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign = "-"
		text = strings.TrimPrefix(text, "-")
	}
	if index := strings.IndexByte(text, '.'); index >= 0 {
		scale.Sub(scale, big.NewInt(int64(len(text)-index-1)))
		text = text[:index] + text[index+1:]
	}
	text = strings.TrimLeft(text, "0")
	if text == "" {
		return "0", new(big.Int)
	}
	digits := strings.TrimRight(text, "0")
	scale.Add(scale, big.NewInt(int64(len(text)-len(digits))))
	return sign + digits, scale
}

func conditionString(value json.RawMessage) string {
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return ""
	}
	return text
}

// Matches evaluates a resolved condition. Missing fields never match, even ne.
func (condition *RecordCondition) Matches(document Document) bool {
	if condition == nil {
		return true
	}
	if condition.All != nil {
		for _, child := range condition.All {
			if !child.Matches(document) {
				return false
			}
		}
		return true
	}
	if condition.Any != nil {
		for _, child := range condition.Any {
			if child.Matches(document) {
				return true
			}
		}
		return false
	}
	var value any
	switch condition.Field {
	case "createdBy":
		if document.CreatedBy == "" {
			return false
		}
		value = document.CreatedBy
	case "id":
		value = document.ID
	default:
		data, err := decodeConditionJSON(document.Data)
		if err != nil {
			return false
		}
		value = data
		for _, part := range strings.Split(strings.TrimPrefix(condition.Field, "data."), ".") {
			object, ok := value.(map[string]any)
			if !ok {
				return false
			}
			value, ok = object[part]
			if !ok {
				return false
			}
		}
	}
	expected, err := decodeConditionJSON(condition.Value)
	if err != nil {
		return false
	}
	switch condition.Op {
	case "eq":
		return equalConditionValue(value, expected)
	case "ne":
		return !equalConditionValue(value, expected)
	case "in":
		values, ok := expected.([]any)
		if ok {
			for _, candidate := range values {
				if equalConditionValue(value, candidate) {
					return true
				}
			}
		}
	case "contains":
		values, ok := value.([]any)
		if ok {
			for _, candidate := range values {
				if equalConditionValue(candidate, expected) {
					return true
				}
			}
		}
	}
	return false
}

func resolveRecordCondition(condition *RecordCondition, identity *Identity) *RecordCondition {
	if condition == nil {
		return nil
	}
	result := *condition
	result.All = nil
	result.Any = nil
	for _, child := range condition.All {
		result.All = append(result.All, *resolveRecordCondition(&child, identity))
	}
	for _, child := range condition.Any {
		result.Any = append(result.Any, *resolveRecordCondition(&child, identity))
	}
	if conditionString(condition.Value) == "$actor.id" {
		if identity == nil {
			return &RecordCondition{Field: "id", Op: "in", Value: json.RawMessage(`[]`)}
		}
		result.Value, _ = json.Marshal(identity.ID)
	}
	return &result
}

func andConditions(conditions ...*RecordCondition) *RecordCondition {
	var all []RecordCondition
	for _, condition := range conditions {
		if condition != nil {
			all = append(all, *condition)
		}
	}
	if len(all) == 0 {
		return nil
	}
	if len(all) == 1 {
		return &all[0]
	}
	return &RecordCondition{All: all}
}

func changedFieldsAllowed(before, after json.RawMessage, fields []string) bool {
	if fields == nil {
		return true
	}
	var old, next map[string]json.RawMessage
	if before != nil {
		if err := json.Unmarshal(before, &old); err != nil {
			return false
		}
	}
	if after != nil {
		if err := json.Unmarshal(after, &next); err != nil {
			return false
		}
	}
	for key := range old {
		if !slices.Contains(fields, key) && !equalRawJSON(old[key], next[key]) {
			return false
		}
	}
	for key := range next {
		if !slices.Contains(fields, key) && !equalRawJSON(old[key], next[key]) {
			return false
		}
	}
	return true
}

func equalRawJSON(left, right json.RawMessage) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	a, errA := decodeConditionJSON(left)
	b, errB := decodeConditionJSON(right)
	return errA == nil && errB == nil && equalConditionValue(a, b)
}

// ValidateDocumentData is used by guarded providers after a mutation callback.
func ValidateDocumentData(data json.RawMessage) error {
	var object map[string]json.RawMessage
	if len(data) > 1<<20 || json.Unmarshal(data, &object) != nil || object == nil {
		return fmt.Errorf("expected a JSON object of at most 1 MiB")
	}
	return nil
}
