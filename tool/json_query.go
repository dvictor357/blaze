package tool

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/dvictor357/blaze/adapter"
)

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
			"required": []string{"json", "query"},
		},
		func(input json.RawMessage) (any, error) {
			var data struct {
				JSON   string `json:"json"`
				Query  string `json:"query"`
				Action string `json:"action"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				return nil, fmt.Errorf("invalid input: %w", err)
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

// splitQueryPath splits a query path into parts, handling array notation
func splitQueryPath(query string) []string {
	var parts []string
	var current strings.Builder
	inBracket := false

	for _, ch := range query {
		switch ch {
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
		var results []any
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
		var values []any
		for _, val := range v {
			values = append(values, val)
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
	start := 0
	end := len(arr)

	if parts[0] != "" {
		var err error
		start, err = strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid slice start: %s", parts[0])
		}
	}

	if len(parts) > 1 && parts[1] != "" {
		var err error
		end, err = strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid slice end: %s", parts[1])
		}
	}

	// Bounds checking
	if start < 0 {
		start = 0
	}
	if end > len(arr) {
		end = len(arr)
	}
	if start > end {
		start = end
	}

	return arr[start:end], nil
}

func filterAccess(data any, condition string) (any, error) {
	arr, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("filter requires array")
	}

	// Parse condition: field=="value" or field==value
	re := regexp.MustCompile(`(\w+)\s*(==|!=|>|<|>=|<=)\s*"?([^"]*)"?`)
	matches := re.FindStringSubmatch(condition)
	if len(matches) < 4 {
		return nil, fmt.Errorf("invalid filter condition: %s", condition)
	}

	field := matches[1]
	op := matches[2]
	value := matches[3]

	var results []any
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
		return len(v), nil
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

	var result []any
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
	var result []any

	for _, item := range arr {
		key := fmt.Sprintf("%v", item)
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}

	return result, nil
}
