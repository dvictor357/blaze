package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file implements runtime validation of tool-call arguments against a
// tool's InputSchema. It supports the JSON Schema keywords tools commonly use:
//
//	type (string or array; "integer" accepts 1 and 1.0), enum, const,
//	properties, required, additionalProperties (bool or schema),
//	minProperties, maxProperties, items (single schema), minItems, maxItems,
//	uniqueItems, minLength, maxLength (in Unicode code points), pattern
//	(Go RE2 syntax, unanchored), minimum, maximum, exclusiveMinimum,
//	exclusiveMaximum (numeric form), multipleOf, allOf, anyOf, oneOf, not.
//
// Annotations such as description, title, default, examples and format are
// ignored, as JSON Schema allows. Keywords that would change the meaning of a
// schema but are not implemented ($ref, if/then/else, patternProperties, ...)
// make NewExecutor fail rather than be silently skipped; set
// Config.DisableSchemaValidation to register such tools without validation.

// maxSchemaErrors bounds how many violations one failed validation reports.
const maxSchemaErrors = 8

// unsupportedKeywords change validation semantics but are not implemented.
var unsupportedKeywords = []string{
	"$ref", "$dynamicRef", "$recursiveRef",
	"if", "then", "else",
	"dependentSchemas", "dependentRequired", "dependencies",
	"patternProperties", "propertyNames",
	"contains", "prefixItems", "additionalItems",
	"unevaluatedProperties", "unevaluatedItems",
}

var schemaTypes = map[string]bool{
	"null": true, "boolean": true, "object": true, "array": true,
	"number": true, "integer": true, "string": true,
}

// schemaNode is a compiled schema. Nil pointers and empty slices mean the
// keyword is absent. A node with reject set matches nothing (schema false).
type schemaNode struct {
	reject bool

	types    []string
	enum     []any
	hasConst bool
	constVal any

	properties    map[string]*schemaNode
	required      []string
	additional    *schemaNode
	minProperties *int
	maxProperties *int

	items       *schemaNode
	minItems    *int
	maxItems    *int
	uniqueItems bool

	minLength *int
	maxLength *int
	pattern   *regexp.Regexp

	minimum          *float64
	maximum          *float64
	exclusiveMinimum *float64
	exclusiveMaximum *float64
	multipleOf       *float64

	allOf []*schemaNode
	anyOf []*schemaNode
	oneOf []*schemaNode
	not   *schemaNode
}

// compileSchema compiles a JSON-decoded schema (bool or object). path names
// the location for registration errors, for example "properties.city".
func compileSchema(v any, path string) (*schemaNode, error) {
	switch s := v.(type) {
	case bool:
		return &schemaNode{reject: !s}, nil
	case map[string]any:
		return compileObject(s, path)
	default:
		return nil, fmt.Errorf("schema at %s must be an object or a boolean, got %s", pathOrRoot(path), jsonKind(v))
	}
}

func compileObject(s map[string]any, path string) (*schemaNode, error) {
	for _, k := range unsupportedKeywords {
		if _, ok := s[k]; ok {
			return nil, fmt.Errorf("schema at %s uses %q, which runtime validation does not support "+
				"(inline the definition, or set Config.DisableSchemaValidation)", pathOrRoot(path), k)
		}
	}
	at := func(keyword string) string { return joinSchemaPath(path, keyword) }
	n := &schemaNode{}
	var err error

	if t, ok := s["type"]; ok {
		switch t := t.(type) {
		case string:
			n.types = []string{t}
		case []any:
			for _, x := range t {
				name, ok := x.(string)
				if !ok {
					return nil, fmt.Errorf("schema at %s: type entries must be strings", at("type"))
				}
				n.types = append(n.types, name)
			}
		default:
			return nil, fmt.Errorf("schema at %s must be a string or an array of strings", at("type"))
		}
		for _, name := range n.types {
			if !schemaTypes[name] {
				return nil, fmt.Errorf("schema at %s: unknown type %q", at("type"), name)
			}
		}
	}
	if e, ok := s["enum"]; ok {
		list, ok := e.([]any)
		if !ok {
			return nil, fmt.Errorf("schema at %s must be an array", at("enum"))
		}
		n.enum = list
	}
	if c, ok := s["const"]; ok {
		n.hasConst, n.constVal = true, c
	}

	if p, ok := s["properties"]; ok {
		props, ok := p.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema at %s must be an object", at("properties"))
		}
		n.properties = make(map[string]*schemaNode, len(props))
		for name, sub := range props {
			if n.properties[name], err = compileSchema(sub, joinSchemaPath(at("properties"), name)); err != nil {
				return nil, err
			}
		}
	}
	if r, ok := s["required"]; ok {
		list, ok := r.([]any)
		if !ok {
			return nil, fmt.Errorf("schema at %s must be an array of strings", at("required"))
		}
		for _, x := range list {
			name, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("schema at %s must be an array of strings", at("required"))
			}
			n.required = append(n.required, name)
		}
	}
	if a, ok := s["additionalProperties"]; ok {
		if n.additional, err = compileSchema(a, at("additionalProperties")); err != nil {
			return nil, err
		}
	}
	if it, ok := s["items"]; ok {
		if _, isArray := it.([]any); isArray {
			return nil, fmt.Errorf("schema at %s: tuple-form items is not supported (use a single schema)", at("items"))
		}
		if n.items, err = compileSchema(it, at("items")); err != nil {
			return nil, err
		}
	}
	if u, ok := s["uniqueItems"]; ok {
		b, ok := u.(bool)
		if !ok {
			return nil, fmt.Errorf("schema at %s must be a boolean", at("uniqueItems"))
		}
		n.uniqueItems = b
	}
	if p, ok := s["pattern"]; ok {
		str, ok := p.(string)
		if !ok {
			return nil, fmt.Errorf("schema at %s must be a string", at("pattern"))
		}
		if n.pattern, err = regexp.Compile(str); err != nil {
			return nil, fmt.Errorf("schema at %s: invalid regular expression: %w", at("pattern"), err)
		}
	}

	counts := []struct {
		name string
		dst  **int
	}{
		{"minProperties", &n.minProperties}, {"maxProperties", &n.maxProperties},
		{"minItems", &n.minItems}, {"maxItems", &n.maxItems},
		{"minLength", &n.minLength}, {"maxLength", &n.maxLength},
	}
	for _, c := range counts {
		if v, ok := s[c.name]; ok {
			f, ok := v.(float64)
			if !ok || f < 0 || f != math.Trunc(f) || f > math.MaxInt32 {
				return nil, fmt.Errorf("schema at %s must be a non-negative integer", at(c.name))
			}
			i := int(f)
			*c.dst = &i
		}
	}
	bounds := []struct {
		name string
		dst  **float64
	}{
		{"minimum", &n.minimum}, {"maximum", &n.maximum},
		{"exclusiveMinimum", &n.exclusiveMinimum}, {"exclusiveMaximum", &n.exclusiveMaximum},
		{"multipleOf", &n.multipleOf},
	}
	for _, b := range bounds {
		if v, ok := s[b.name]; ok {
			f, ok := v.(float64)
			if !ok {
				return nil, fmt.Errorf("schema at %s must be a number", at(b.name))
			}
			*b.dst = &f
		}
	}
	if n.multipleOf != nil && *n.multipleOf <= 0 {
		return nil, fmt.Errorf("schema at %s must be greater than 0", at("multipleOf"))
	}

	lists := []struct {
		name string
		dst  *[]*schemaNode
	}{{"allOf", &n.allOf}, {"anyOf", &n.anyOf}, {"oneOf", &n.oneOf}}
	for _, l := range lists {
		v, ok := s[l.name]
		if !ok {
			continue
		}
		subs, ok := v.([]any)
		if !ok || len(subs) == 0 {
			return nil, fmt.Errorf("schema at %s must be a non-empty array of schemas", at(l.name))
		}
		for i, sub := range subs {
			node, err := compileSchema(sub, joinSchemaPath(at(l.name), strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
			*l.dst = append(*l.dst, node)
		}
	}
	if v, ok := s["not"]; ok {
		if n.not, err = compileSchema(v, at("not")); err != nil {
			return nil, err
		}
	}
	return n, nil
}

// validateInput decodes a JSON object input and checks it against the
// compiled schema. It returns nil when the input conforms, otherwise an
// error listing up to maxSchemaErrors violations with their JSON paths.
func (n *schemaNode) validateInput(input json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %v", err)
	}
	var errs []string
	n.validate(value, "$", &errs)
	if len(errs) == 0 {
		return nil
	}
	msg := "arguments do not match the input schema: " + strings.Join(errs, "; ")
	if len(errs) >= maxSchemaErrors {
		msg += "; (further errors omitted)"
	}
	return fmt.Errorf("%s", msg)
}

func (n *schemaNode) validate(v any, path string, errs *[]string) {
	add := func(format string, args ...any) {
		if len(*errs) < maxSchemaErrors {
			*errs = append(*errs, path+": "+fmt.Sprintf(format, args...))
		}
	}
	if n.reject {
		add("no value is allowed here")
		return
	}
	if len(n.types) > 0 && !matchesAnyType(v, n.types) {
		add("expected %s, got %s", strings.Join(n.types, " or "), jsonKind(v))
		return // further checks would only repeat the type mismatch
	}
	if n.enum != nil {
		found := false
		for _, e := range n.enum {
			if jsonEqual(v, e) {
				found = true
				break
			}
		}
		if !found {
			add("must be one of %s, got %s", compactJSON(n.enum), compactJSON(v))
		}
	}
	if n.hasConst && !jsonEqual(v, n.constVal) {
		add("must equal %s, got %s", compactJSON(n.constVal), compactJSON(v))
	}

	switch v := v.(type) {
	case map[string]any:
		n.validateObject(v, path, errs, add)
	case []any:
		n.validateArray(v, path, errs, add)
	case string:
		length := utf8.RuneCountInString(v)
		if n.minLength != nil && length < *n.minLength {
			add("must be at least %d characters long, got %d", *n.minLength, length)
		}
		if n.maxLength != nil && length > *n.maxLength {
			add("must be at most %d characters long, got %d", *n.maxLength, length)
		}
		if n.pattern != nil && !n.pattern.MatchString(v) {
			add("must match pattern %q", n.pattern.String())
		}
	case json.Number:
		n.validateNumber(v, add)
	}

	for _, sub := range n.allOf {
		sub.validate(v, path, errs)
	}
	if len(n.anyOf) > 0 {
		ok := false
		for _, sub := range n.anyOf {
			if sub.matches(v) {
				ok = true
				break
			}
		}
		if !ok {
			add("must match at least one of the anyOf schemas")
		}
	}
	if len(n.oneOf) > 0 {
		matched := 0
		for _, sub := range n.oneOf {
			if sub.matches(v) {
				matched++
			}
		}
		if matched != 1 {
			add("must match exactly one of the oneOf schemas, matched %d", matched)
		}
	}
	if n.not != nil && n.not.matches(v) {
		add("must not match the schema in \"not\"")
	}
}

func (n *schemaNode) validateObject(obj map[string]any, path string, errs *[]string, add func(string, ...any)) {
	for _, name := range n.required {
		if _, ok := obj[name]; !ok {
			add("missing required property %q", name)
		}
	}
	if n.minProperties != nil && len(obj) < *n.minProperties {
		add("must have at least %d properties, got %d", *n.minProperties, len(obj))
	}
	if n.maxProperties != nil && len(obj) > *n.maxProperties {
		add("must have at most %d properties, got %d", *n.maxProperties, len(obj))
	}
	// Sorted keys keep error messages deterministic.
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if sub, ok := n.properties[k]; ok {
			sub.validate(obj[k], childPath(path, k), errs)
			continue
		}
		if n.additional == nil {
			continue
		}
		if n.additional.reject {
			add("unknown property %q", k)
			continue
		}
		n.additional.validate(obj[k], childPath(path, k), errs)
	}
}

func (n *schemaNode) validateArray(arr []any, path string, errs *[]string, add func(string, ...any)) {
	if n.minItems != nil && len(arr) < *n.minItems {
		add("must have at least %d items, got %d", *n.minItems, len(arr))
	}
	if n.maxItems != nil && len(arr) > *n.maxItems {
		add("must have at most %d items, got %d", *n.maxItems, len(arr))
	}
	if n.uniqueItems {
	outer:
		for i := range arr {
			for j := i + 1; j < len(arr); j++ {
				if jsonEqual(arr[i], arr[j]) {
					add("items must be unique (items %d and %d are equal)", i, j)
					break outer
				}
			}
		}
	}
	if n.items != nil {
		for i, item := range arr {
			n.items.validate(item, path+"["+strconv.Itoa(i)+"]", errs)
		}
	}
}

func (n *schemaNode) validateNumber(num json.Number, add func(string, ...any)) {
	f, err := strconv.ParseFloat(string(num), 64)
	if err != nil { // out of float64 range
		add("number %s is out of range", num)
		return
	}
	if n.minimum != nil && f < *n.minimum {
		add("must be >= %v, got %s", *n.minimum, num)
	}
	if n.maximum != nil && f > *n.maximum {
		add("must be <= %v, got %s", *n.maximum, num)
	}
	if n.exclusiveMinimum != nil && f <= *n.exclusiveMinimum {
		add("must be > %v, got %s", *n.exclusiveMinimum, num)
	}
	if n.exclusiveMaximum != nil && f >= *n.exclusiveMaximum {
		add("must be < %v, got %s", *n.exclusiveMaximum, num)
	}
	if n.multipleOf != nil {
		q := f / *n.multipleOf
		if math.IsInf(q, 0) || math.Abs(q-math.Round(q)) > 1e-9 {
			add("must be a multiple of %v, got %s", *n.multipleOf, num)
		}
	}
}

// matches reports whether v conforms, without collecting messages.
func (n *schemaNode) matches(v any) bool {
	var errs []string
	n.validate(v, "$", &errs)
	return len(errs) == 0
}

func matchesAnyType(v any, types []string) bool {
	for _, t := range types {
		switch t {
		case "null":
			if v == nil {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "number":
			if _, ok := v.(json.Number); ok {
				return true
			}
		case "integer":
			if num, ok := v.(json.Number); ok && isInteger(num) {
				return true
			}
		}
	}
	return false
}

// isInteger reports whether a JSON number has no fractional part, so 3 and
// 3.0 are integers, as JSON Schema specifies.
func isInteger(num json.Number) bool {
	if !strings.ContainsAny(string(num), ".eE") {
		return true
	}
	f, err := strconv.ParseFloat(string(num), 64)
	return err == nil && !math.IsInf(f, 0) && f == math.Trunc(f)
}

func jsonKind(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case json.Number:
		if isInteger(v) {
			return "integer"
		}
		return "number"
	case float64:
		return "number"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// jsonEqual compares two JSON values. Input numbers are json.Number and
// schema numbers are float64; numbers compare by value.
func jsonEqual(a, b any) bool {
	if fa, ok := numberValue(a); ok {
		fb, ok := numberValue(b)
		return ok && fa == fb
	}
	switch a := a.(type) {
	case nil:
		return b == nil
	case bool:
		bb, ok := b.(bool)
		return ok && a == bb
	case string:
		bs, ok := b.(string)
		return ok && a == bs
	case []any:
		bl, ok := b.([]any)
		if !ok || len(a) != len(bl) {
			return false
		}
		for i := range a {
			if !jsonEqual(a[i], bl[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bm, ok := b.(map[string]any)
		if !ok || len(a) != len(bm) {
			return false
		}
		for k, av := range a {
			bv, ok := bm[k]
			if !ok || !jsonEqual(av, bv) {
				return false
			}
		}
		return true
	}
	return false
}

func numberValue(v any) (float64, bool) {
	switch v := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil
	case float64:
		return v, true
	}
	return 0, false
}

// compactJSON renders a value for an error message, truncated so that a
// huge enum or input cannot blow up the message.
func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	const limit = 200
	if len(b) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		return string(b[:cut]) + "…"
	}
	return string(b)
}

var simplePropertyName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func childPath(path, key string) string {
	if simplePropertyName.MatchString(key) {
		return path + "." + key
	}
	return path + "[" + strconv.Quote(key) + "]"
}

func joinSchemaPath(path, elem string) string {
	if path == "" {
		return elem
	}
	return path + "." + elem
}

func pathOrRoot(path string) string {
	if path == "" {
		return "the root"
	}
	return path
}
