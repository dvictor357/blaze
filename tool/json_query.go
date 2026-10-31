package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dvictor357/blaze/adapter"
)

// filterPattern parses a filter condition such as `status=="active"` or
// `age>=18`. Two-character operators must come first.
var filterPattern = regexp.MustCompile(`^\s*(\w+)\s*(==|!=|>=|<=|>|<)\s*("(?:[^"\\]|\\.)*"|[^"]*)\s*$`)

// NewJSONQueryTool creates a tool for querying and transforming JSON data.
// It provides jq-like functionality for extracting values from JSON.
// Supports:
// - Dot notation: .field.nested
// - Array indexing: .array[0]
// - Array slicing: .array[0:3]
// - Wildcards: .array[*].name
// - Filtering: .array[?name=="foo"]
func NewJSONQueryTool() adapter.Tool {
	return adapter.NewTool(
		"json_query",
		"Query and extract data from JSON. Use dot notation to access fields (e.g., '.data.users[0].name'). Supports array indexing, slicing, wildcards, and filtering. Use this to parse API responses or extract specific fields from JSON data.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"json": map[string]any{
					"type":        "string",
					"description": "The JSON string to query",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Query path using dot notation (e.g., '.data.items[0].name', '.users[*].email', '.items[?status==\"active\"]')",
				},
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"get", "keys", "length", "type", "flatten", "unique"},
					"description": "Action: 'get' (extract value), 'keys' (list keys), 'length' (count items), 'type' (get type), 'flatten' (flatten array), 'unique' (deduplicate array)",
				},
			},
			"required":             []string{"json", "query"},
			"additionalProperties": false,
		},
		func(ctx context.Context, input json.RawMessage) (any, error) {
			var data struct {
				JSON   string `json:"json"`
				Query  string `json:"query"`
				Action string `json:"action"`
			}
			if err := decodeInput(input, &data); err != nil {
				return nil, err
			}

			if data.JSON == "" {
				return nil, fmt.Errorf("json cannot be empty")
			}

			if data.Action == "" {
				data.Action = "get"
			}

			// Parse the JSON
			var jsonData any
			if err := json.Unmarshal([]byte(data.JSON), &jsonData); err != nil {
				return nil, fmt.Errorf("invalid JSON: %w", err)
			}

			// Execute the query
			result, err := executeQuery(jsonData, data.Query)
			if err != nil {
				return nil, err
			}

			// Apply action
			switch data.Action {
			case "get":
				return map[string]any{
					"result": result,
					"query":  data.Query,
				}, nil

			case "keys":
				keys, err := getKeys(result)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"keys":  keys,
					"count": len(keys),
				}, nil

			case "length":
				length, err := getLength(result)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"length": length,
				}, nil

			case "type":
				return map[string]any{
					"type": getType(result),
				}, nil

			case "flatten":
				flattened, err := flatten(result)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"result": flattened,
				}, nil

			case "unique":
				unique, err := uniqueValues(result)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"result": unique,
				}, nil

			default:
				return nil, fmt.Errorf("unknown action: %s", data.Action)
			}
		},
	)
}

// executeQuery parses and executes a query path on JSON data
func executeQuery(data any, query string) (any, error) {
	if query == "" || query == "." {
		return data, nil
	}

	if err := checkBrackets(query); err != nil {
		return nil, err
	}

	// Remove leading dot if present
	query = strings.TrimPrefix(query, ".")

	// Split query into parts, handling array notation
	parts := splitQueryPath(query)

	current := data
	for _, part := range parts {
		var err error
		current, err = accessField(current, part)
		if err != nil {
			return nil, err
		}
	}

	return current, nil
}

// checkBrackets rejects unbalanced or nested brackets, which would otherwise
// be read as odd field names.
func checkBrackets(query string) error {
	open := false
	quoted, escaped := false, false
	for _, ch := range query {
		if quoted {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				quoted = false
			}
			continue
		}
		switch ch {
		case '"':
			quoted = open
		case '[':
			if open {
				return fmt.Errorf("invalid query: nested '['")
			}
			open = true
		case ']':
			if !open {
				return fmt.Errorf("invalid query: unmatched ']'")
			}
			open = false
		}
	}
	if quoted {
		return fmt.Errorf("invalid query: unterminated quoted value")
	}
	if open {
		return fmt.Errorf("invalid query: unterminated '['")
	}
	return nil
}

// splitQueryPath splits a query path into parts, handling array notation
func splitQueryPath(query string) []string {
	var parts []string
	var current strings.Builder
	inBracket := false
	quoted, escaped := false, false

	for _, ch := range query {
		if quoted {
			current.WriteRune(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				quoted = false
			}
			continue
		}
		switch ch {
		case '"':
			quoted = inBracket
			current.WriteRune(ch)
		case '[':
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			inBracket = true
			current.WriteRune(ch)
		case ']':
			current.WriteRune(ch)
			inBracket = false
			parts = append(parts, current.String())
			current.Reset()
		case '.':
			if inBracket {
				current.WriteRune(ch)
			} else if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// accessField accesses a single field or array element
func accessField(data any, field string) (any, error) {
	if data == nil {
		return nil, fmt.Errorf("cannot access '%s' on null", field)
	}

	// Handle array access [n], [n:m], [*], [?filter]
	if strings.HasPrefix(field, "[") && strings.HasSuffix(field, "]") {
		inner := field[1 : len(field)-1]

		// Wildcard [*]
		if inner == "*" {
			return wildcardAccess(data)
		}

		// Filter [?condition]
		if strings.HasPrefix(inner, "?") {
			return filterAccess(data, inner[1:])
		}

		// Slice [n:m]
		if strings.Contains(inner, ":") {
			return sliceAccess(data, inner)
		}

		// Index [n]
		return indexAccess(data, inner)
	}

	// Handle object field access
	switch v := data.(type) {
	case map[string]any:
		if val, ok := v[field]; ok {
			return val, nil
		}
		return nil, fmt.Errorf("field '%s' not found", field)

	case []any:
		// Apply field access to each element (implicit wildcard)
		results := []any{}
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if val, ok := m[field]; ok {
					results = append(results, val)
				}
			}
		}
		return results, nil

	default:
		return nil, fmt.Errorf("cannot access field '%s' on %T", field, data)
	}
}

func wildcardAccess(data any) (any, error) {
	switch v := data.(type) {
	case []any:
		return v, nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic order
		values := make([]any, 0, len(v))
		for _, k := range keys {
			values = append(values, v[k])
		}
		return values, nil
	default:
		return nil, fmt.Errorf("wildcard requires array or object")
	}
}

func indexAccess(data any, indexStr string) (any, error) {
	arr, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("cannot index non-array")
	}

	idx, err := strconv.Atoi(indexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid index: %s", indexStr)
	}

	// Support negative indexing
	if idx < 0 {
		idx = len(arr) + idx
	}

	if idx < 0 || idx >= len(arr) {
		return nil, fmt.Errorf("index %d out of range (length: %d)", idx, len(arr))
	}

	return arr[idx], nil
}

func sliceAccess(data any, sliceStr string) (any, error) {
	arr, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("cannot slice non-array")
	}

	parts := strings.Split(sliceStr, ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid slice [%s]: expected [start:end]", sliceStr)
	}

	n := len(arr)
	start, end := 0, n

	if p := strings.TrimSpace(parts[0]); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid slice start: %s", p)
		}
		start = v
	}
	if p := strings.TrimSpace(parts[1]); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid slice end: %s", p)
		}
		end = v
	}

	// Negative bounds count from the end (as in Python); everything is then
	// clamped to [0, n], and an inverted range is simply empty.
	if start < 0 {
		start += n
	}
	if end < 0 {
		end += n
	}
	start = max(0, min(start, n))
	end = max(0, min(end, n))
	if start >= end {
		return []any{}, nil
	}

	return append([]any{}, arr[start:end]...), nil
}

func filterAccess(data any, condition string) (any, error) {
	arr, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("filter requires array")
	}

	// Parse condition: field=="value" or field==value
	matches := filterPattern.FindStringSubmatch(condition)
	if len(matches) < 4 {
		return nil, fmt.Errorf("invalid filter condition: %s", condition)
	}

	field := matches[1]
	op := matches[2]
	value := matches[3]
	if strings.HasPrefix(value, `"`) {
		var err error
		value, err = strconv.Unquote(value)
		if err != nil {
			return nil, fmt.Errorf("invalid filter value: %w", err)
		}
	}

	results := []any{}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		fieldVal, ok := m[field]
		if !ok {
			continue
		}

		if matchesCondition(fieldVal, op, value) {
			results = append(results, item)
		}
	}

	return results, nil
}

func matchesCondition(fieldVal any, op, value string) bool {
	fieldStr := fmt.Sprintf("%v", fieldVal)

	switch op {
	case "==":
		return fieldStr == value
	case "!=":
		return fieldStr != value
	case ">", "<", ">=", "<=":
		// Try numeric comparison
		fv, err1 := strconv.ParseFloat(fieldStr, 64)
		cv, err2 := strconv.ParseFloat(value, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		switch op {
		case ">":
			return fv > cv
		case "<":
			return fv < cv
		case ">=":
			return fv >= cv
		case "<=":
			return fv <= cv
		}
	}
	return false
}

func getKeys(data any) ([]string, error) {
	switch v := data.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys, nil
	default:
		return nil, fmt.Errorf("keys requires an object, got %T", data)
	}
}

func getLength(data any) (int, error) {
	switch v := data.(type) {
	case []any:
		return len(v), nil
	case map[string]any:
		return len(v), nil
	case string:
		return utf8.RuneCountInString(v), nil
	default:
		return 0, fmt.Errorf("cannot get length of %T", data)
	}
}

func getType(data any) string {
	if data == nil {
		return "null"
	}
	switch data.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	default:
		return reflect.TypeOf(data).String()
	}
}

func flatten(data any) ([]any, error) {
	arr, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("flatten requires an array")
	}

	result := []any{}
	for _, item := range arr {
		if nested, ok := item.([]any); ok {
			result = append(result, nested...)
		} else {
			result = append(result, item)
		}
	}
	return result, nil
}

func uniqueValues(data any) ([]any, error) {
	arr, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("unique requires an array")
	}

	seen := make(map[string]bool)
	result := []any{}

	for _, item := range arr {
		// Encoded JSON distinguishes "1" from 1 and nested values by content.
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("cannot compare values: %w", err)
		}
		key := string(encoded)
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}

	return result, nil
}
