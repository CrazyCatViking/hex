package hex

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Automation templates fill JSON values from earlier steps and the clock.
// Any string may contain {{ expression }} placeholders. A string that is
// exactly one placeholder becomes the expression's JSON value; otherwise
// placeholders are replaced by their text. An expression is a value — a
// path such as steps.issues.output.total or item.name, or a literal such as
// "text", 3 or true — followed by filters: {{ steps.users.output | map("email") | join(", ") }}.
// Missing paths are null rather than errors, so default("…") can fill them.

var placeholderPattern = regexp.MustCompile(`\{\{\s*(.*?)\s*\}\}`)

const maxTemplateDepth = 32

// renderTemplate replaces placeholders throughout a JSON document.
func renderTemplate(document json.RawMessage, data map[string]any) (json.RawMessage, error) {
	if len(document) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	rendered, err := renderValue(value, data, 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rendered)
}

func renderValue(value any, data map[string]any, depth int) (any, error) {
	if depth > maxTemplateDepth {
		return nil, errors.New("the template is nested too deeply")
	}
	switch typed := value.(type) {
	case string:
		return renderString(typed, data)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			rendered, err := renderValue(item, data, depth+1)
			if err != nil {
				return nil, err
			}
			result[index] = rendered
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			rendered, err := renderValue(item, data, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = rendered
		}
		return result, nil
	default:
		return value, nil
	}
}

// renderString evaluates a string's placeholders.
func renderString(text string, data map[string]any) (any, error) {
	matches := placeholderPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}
	whole := len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(text)
	if whole {
		return evaluateExpression(text[matches[0][2]:matches[0][3]], data)
	}

	var builder strings.Builder
	last := 0
	for _, match := range matches {
		builder.WriteString(text[last:match[0]])
		value, err := evaluateExpression(text[match[2]:match[3]], data)
		if err != nil {
			return nil, err
		}
		builder.WriteString(templateText(value))
		last = match[1]
	}
	builder.WriteString(text[last:])
	return builder.String(), nil
}

// renderText evaluates a template whose result is used as text, such as a
// prompt.
func renderText(text string, data map[string]any) (string, error) {
	value, err := renderString(text, data)
	if err != nil {
		return "", err
	}
	return templateText(value), nil
}

// evaluateCondition reports whether a template's value is truthy.
func evaluateCondition(text string, data map[string]any) (bool, error) {
	if !placeholderPattern.MatchString(text) {
		text = "{{ " + text + " }}"
	}
	value, err := renderString(text, data)
	if err != nil {
		return false, err
	}
	return truthy(value), nil
}

func evaluateExpression(expression string, data map[string]any) (any, error) {
	parts, err := splitOutside(expression, '|')
	if err != nil {
		return nil, fmt.Errorf("{{ %s }}: %w", expression, err)
	}
	value, err := evaluateOperand(strings.TrimSpace(parts[0]), data)
	if err != nil {
		return nil, fmt.Errorf("{{ %s }}: %w", expression, err)
	}
	for _, part := range parts[1:] {
		value, err = applyFilter(strings.TrimSpace(part), value, data)
		if err != nil {
			return nil, fmt.Errorf("{{ %s }}: %w", expression, err)
		}
	}
	return value, nil
}

// splitOutside splits on separator outside quotes and parentheses.
func splitOutside(text string, separator rune) ([]string, error) {
	var parts []string
	depth := 0
	quoted := false
	start := 0
	for index, character := range text {
		switch {
		case character == '"' && (index == 0 || text[index-1] != '\\'):
			quoted = !quoted
		case quoted:
		case character == '(':
			depth++
		case character == ')':
			depth--
		case character == separator && depth == 0:
			parts = append(parts, text[start:index])
			start = index + 1
		}
	}
	if quoted || depth != 0 {
		return nil, errors.New("unbalanced quotes or parentheses")
	}
	return append(parts, text[start:]), nil
}

var (
	pathSegmentPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)((?:\[\d+\])*)$`)
	indexPattern       = regexp.MustCompile(`\[(\d+)\]`)
	filterPattern      = regexp.MustCompile(`^([a-zA-Z]+)(?:\((.*)\))?$`)
)

// evaluateOperand reads a literal or a path.
func evaluateOperand(operand string, data map[string]any) (any, error) {
	switch {
	case operand == "":
		return nil, errors.New("missing value")
	case strings.HasPrefix(operand, `"`):
		var text string
		if err := json.Unmarshal([]byte(operand), &text); err != nil {
			return nil, fmt.Errorf("invalid string %s", operand)
		}
		return text, nil
	case operand == "true":
		return true, nil
	case operand == "false":
		return false, nil
	case operand == "null":
		return nil, nil
	}
	if number, err := strconv.ParseFloat(operand, 64); err == nil {
		return number, nil
	}
	return lookupPath(operand, data)
}

func lookupPath(path string, data map[string]any) (any, error) {
	var current any = data
	for _, segment := range strings.Split(path, ".") {
		match := pathSegmentPattern.FindStringSubmatch(segment)
		if match == nil {
			return nil, fmt.Errorf("invalid path %q", path)
		}
		object, isObject := current.(map[string]any)
		if !isObject {
			return nil, nil
		}
		current = object[match[1]]
		for _, index := range indexPattern.FindAllStringSubmatch(match[2], -1) {
			position, _ := strconv.Atoi(index[1])
			list, isList := current.([]any)
			if !isList || position >= len(list) {
				return nil, nil
			}
			current = list[position]
		}
	}
	return current, nil
}

func applyFilter(filter string, value any, data map[string]any) (any, error) {
	match := filterPattern.FindStringSubmatch(filter)
	if match == nil {
		return nil, fmt.Errorf("invalid filter %q", filter)
	}
	name := match[1]
	var arguments []any
	if strings.TrimSpace(match[2]) != "" {
		parts, err := splitOutside(match[2], ',')
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			argument, err := evaluateOperand(strings.TrimSpace(part), data)
			if err != nil {
				return nil, err
			}
			arguments = append(arguments, argument)
		}
	}
	argument := func(index int) any {
		if index < len(arguments) {
			return arguments[index]
		}
		return nil
	}

	switch name {
	case "length":
		switch typed := value.(type) {
		case []any:
			return float64(len(typed)), nil
		case map[string]any:
			return float64(len(typed)), nil
		case string:
			return float64(len([]rune(typed))), nil
		}
		return float64(0), nil
	case "json":
		encoded, err := json.Marshal(value)
		return string(encoded), err
	case "join":
		separator := ", "
		if len(arguments) > 0 {
			separator = templateText(argument(0))
		}
		items := asList(value)
		texts := make([]string, len(items))
		for index, item := range items {
			texts[index] = templateText(item)
		}
		return strings.Join(texts, separator), nil
	case "default":
		if truthy(value) {
			return value, nil
		}
		return argument(0), nil
	case "first", "last":
		items := asList(value)
		if len(items) == 0 {
			return nil, nil
		}
		if name == "first" {
			return items[0], nil
		}
		return items[len(items)-1], nil
	case "limit":
		items := asList(value)
		count := int(asNumber(argument(0)))
		if count < len(items) && count >= 0 {
			return items[:count], nil
		}
		return items, nil
	case "upper":
		return strings.ToUpper(templateText(value)), nil
	case "lower":
		return strings.ToLower(templateText(value)), nil
	case "truncate":
		runes := []rune(templateText(value))
		count := int(asNumber(argument(0)))
		if count >= 0 && count < len(runes) {
			return string(runes[:count]) + "…", nil
		}
		return string(runes), nil
	case "map":
		field := templateText(argument(0))
		items := asList(value)
		result := make([]any, 0, len(items))
		for _, item := range items {
			object, _ := item.(map[string]any)
			picked, err := lookupPath(field, object)
			if err != nil {
				return nil, err
			}
			result = append(result, picked)
		}
		return result, nil
	case "where":
		field := templateText(argument(0))
		wanted := argument(1)
		result := make([]any, 0)
		for _, item := range asList(value) {
			object, _ := item.(map[string]any)
			picked, err := lookupPath(field, object)
			if err != nil {
				return nil, err
			}
			if len(arguments) < 2 && truthy(picked) || len(arguments) >= 2 && templateText(picked) == templateText(wanted) {
				result = append(result, item)
			}
		}
		return result, nil
	case "sum":
		total := 0.0
		for _, item := range asList(value) {
			if len(arguments) > 0 {
				object, _ := item.(map[string]any)
				picked, err := lookupPath(templateText(argument(0)), object)
				if err != nil {
					return nil, err
				}
				item = picked
			}
			total += asNumber(item)
		}
		return total, nil
	case "round":
		scale := math.Pow(10, asNumber(argument(0)))
		return math.Round(asNumber(value)*scale) / scale, nil
	case "eq":
		return templateText(value) == templateText(argument(0)), nil
	case "ne":
		return templateText(value) != templateText(argument(0)), nil
	case "gt":
		return asNumber(value) > asNumber(argument(0)), nil
	case "lt":
		return asNumber(value) < asNumber(argument(0)), nil
	case "not":
		return !truthy(value), nil
	case "date":
		return formatDate(value, templateText(argument(0)))
	default:
		return nil, fmt.Errorf("unknown filter %q", name)
	}
}

var dateTokens = strings.NewReplacer("YYYY", "2006", "MM", "01", "DD", "02", "HH", "15", "mm", "04", "ss", "05")

// formatDate formats an RFC 3339 timestamp or YYYY-MM-DD date with tokens
// YYYY, MM, DD, HH, mm and ss.
func formatDate(value any, format string) (any, error) {
	text := templateText(value)
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		parsed, err = time.Parse(time.DateOnly, text)
	}
	if err != nil {
		return nil, fmt.Errorf("date: %q is not a date", text)
	}
	if format == "" {
		format = "YYYY-MM-DD"
	}
	return parsed.Format(dateTokens.Replace(format)), nil
}

func asList(value any) []any {
	list, _ := value.([]any)
	return list
}

func asNumber(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case bool:
		if typed {
			return 1
		}
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return number
		}
	}
	return 0
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed != "" && typed != "false"
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

func templateText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// clockValues describes the run's time in the automation's time zone.
func clockValues(now time.Time) map[string]any {
	weekday := (int(now.Weekday()) + 6) % 7
	weekStart := now.AddDate(0, 0, -weekday)
	year, week := now.ISOWeek()
	return map[string]any{
		"iso":               now.Format(time.RFC3339),
		"date":              now.Format(time.DateOnly),
		"time":              now.Format("15:04"),
		"year":              float64(now.Year()),
		"month":             float64(now.Month()),
		"day":               float64(now.Day()),
		"monthDay":          now.Format("01-02"),
		"weekday":           now.Weekday().String(),
		"week":              float64(week),
		"weekYear":          float64(year),
		"yesterday":         now.AddDate(0, 0, -1).Format(time.DateOnly),
		"weekStart":         weekStart.Format(time.DateOnly),
		"previousWeekStart": weekStart.AddDate(0, 0, -7).Format(time.DateOnly),
		"previousWeekEnd":   weekStart.AddDate(0, 0, -1).Format(time.DateOnly),
		"unix":              float64(now.Unix()),
	}
}

// templateReferences lists the step IDs a template refers to, for checking
// that steps only use earlier steps.
func templateReferences(text string) []string {
	var references []string
	for _, match := range placeholderPattern.FindAllStringSubmatch(text, -1) {
		for _, found := range stepReferencePattern.FindAllStringSubmatch(match[1], -1) {
			if !slices.Contains(references, found[1]) {
				references = append(references, found[1])
			}
		}
	}
	return references
}

var stepReferencePattern = regexp.MustCompile(`\bsteps\.([A-Za-z_][A-Za-z0-9_-]*)`)
