package hex

import (
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

type automationResultLimitKey struct{}

// Preflight automation results before json.Marshal allocates its complete
// buffer. Custom marshalers cannot be bounded, so only RawMessage and Time
// are accepted on this path. Ordinary HTTP calls keep their existing contract.
func marshalHostResult(ctx context.Context, value any) ([]byte, error) {
	if bounded, _ := ctx.Value(automationResultLimitKey{}).(bool); bounded {
		budget := resultBudget{ctx: ctx, remaining: maxAutomationOutput, nodes: 65536}
		if err := budget.check(reflect.ValueOf(value), 0); err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(value)
	if bounded, _ := ctx.Value(automationResultLimitKey{}).(bool); bounded && len(data) > maxAutomationOutput {
		return nil, errors.New("automation result exceeds 1 MiB")
	}
	return data, err
}

type resultBudget struct {
	ctx       context.Context
	remaining int
	nodes     int
}

func (b *resultBudget) charge(size int) error {
	b.remaining -= size
	b.nodes--
	if b.remaining < 0 || b.nodes < 0 {
		return errors.New("automation result exceeds its size or complexity limit")
	}
	return b.ctx.Err()
}

func (b *resultBudget) check(value reflect.Value, depth int) error {
	if err := b.charge(2); err != nil {
		return err
	}
	if depth > 32 {
		return errors.New("automation result is nested too deeply")
	}
	if !value.IsValid() {
		return nil
	}
	if value.CanInterface() {
		switch typed := value.Interface().(type) {
		case json.RawMessage:
			return b.charge(len(typed))
		case time.Time, *time.Time:
			return b.charge(64)
		case json.Marshaler, encoding.TextMarshaler:
			return errors.New("custom result marshalers are unavailable in automations")
		}
	}
	if value.CanAddr() && value.Addr().CanInterface() {
		switch value.Addr().Interface().(type) {
		case json.Marshaler, encoding.TextMarshaler:
			return errors.New("custom result marshalers are unavailable in automations")
		}
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			return b.check(value.Elem(), depth+1)
		}
	case reflect.String:
		return b.charge(value.Len())
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return b.charge(value.Len())
		}
		for i := 0; i < value.Len(); i++ {
			if err := b.check(value.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if err := b.check(iterator.Key(), depth+1); err != nil {
				return err
			}
			if err := b.check(iterator.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			if err := b.charge(len(name)); err != nil {
				return err
			}
			if err := b.check(value.Field(i), depth+1); err != nil {
				return err
			}
		}
	default:
		return b.charge(24)
	}
	return nil
}
