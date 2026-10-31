package adapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func mustCompile(t *testing.T, schema string) *schemaNode {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(schema), &v); err != nil {
		t.Fatalf("bad test schema: %v", err)
	}
	n, err := compileSchema(v, "")
	if err != nil {
		t.Fatalf("compileSchema: %v", err)
	}
	return n
}

func TestSchemaValidation_Keywords(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		input   string
		wantErr []string // substrings; nil means the input must be valid
	}{
		{"empty object schema accepts anything", `{"type":"object"}`, `{"a":[1,{"b":null}]}`, nil},
		{"required present", `{"type":"object","required":["a"]}`, `{"a":1}`, nil},
		{"required missing", `{"type":"object","required":["a","b"]}`, `{"a":1}`,
			[]string{`$: missing required property "b"`}},
		{"string type", `{"type":"object","properties":{"s":{"type":"string"}}}`, `{"s":5}`,
			[]string{"$.s: expected string, got integer"}},
		{"integer accepts 2.0", `{"type":"object","properties":{"n":{"type":"integer"}}}`, `{"n":2.0}`, nil},
		{"integer accepts 1e3", `{"type":"object","properties":{"n":{"type":"integer"}}}`, `{"n":1e3}`, nil},
		{"integer rejects 2.5", `{"type":"object","properties":{"n":{"type":"integer"}}}`, `{"n":2.5}`,
			[]string{"$.n: expected integer, got number"}},
		{"integer accepts huge", `{"type":"object","properties":{"n":{"type":"integer"}}}`, `{"n":123456789012345678901234567890}`, nil},
		{"number accepts integer", `{"type":"object","properties":{"n":{"type":"number"}}}`, `{"n":3}`, nil},
		{"type list", `{"type":"object","properties":{"v":{"type":["string","null"]}}}`, `{"v":null}`, nil},
		{"type list mismatch", `{"type":"object","properties":{"v":{"type":["string","null"]}}}`, `{"v":true}`,
			[]string{"expected string or null, got boolean"}},
		{"enum ok", `{"type":"object","properties":{"a":{"enum":["x","y"]}}}`, `{"a":"y"}`, nil},
		{"enum bad", `{"type":"object","properties":{"a":{"enum":["x","y"]}}}`, `{"a":"z"}`,
			[]string{`$.a: must be one of ["x","y"], got "z"`}},
		{"enum numbers compare by value", `{"type":"object","properties":{"a":{"enum":[1,2]}}}`, `{"a":2.0}`, nil},
		{"const", `{"type":"object","properties":{"a":{"const":{"k":[1]}}}}`, `{"a":{"k":[2]}}`,
			[]string{`must equal {"k":[1]}`}},
		{"additionalProperties false", `{"type":"object","properties":{"a":{}},"additionalProperties":false}`, `{"a":1,"b":2}`,
			[]string{`$: unknown property "b"`}},
		{"additionalProperties schema", `{"type":"object","additionalProperties":{"type":"string"}}`, `{"x":"ok","y":1}`,
			[]string{"$.y: expected string, got integer"}},
		{"odd key path is quoted", `{"type":"object","additionalProperties":{"type":"string"}}`, `{"a b":1}`,
			[]string{`$["a b"]: expected string`}},
		{"min/max properties", `{"type":"object","minProperties":2}`, `{"a":1}`,
			[]string{"at least 2 properties"}},
		{"nested object", `{"type":"object","properties":{"o":{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}}}`,
			`{"o":{"id":"x"}}`, []string{"$.o.id: expected integer, got string"}},
		{"array items", `{"type":"object","properties":{"l":{"type":"array","items":{"type":"string"}}}}`, `{"l":["a",2]}`,
			[]string{"$.l[1]: expected string, got integer"}},
		{"array bounds", `{"type":"object","properties":{"l":{"type":"array","minItems":1,"maxItems":2}}}`, `{"l":[1,2,3]}`,
			[]string{"at most 2 items, got 3"}},
		{"uniqueItems", `{"type":"object","properties":{"l":{"type":"array","uniqueItems":true}}}`, `{"l":[1,{"a":1},{"a":1.0}]}`,
			[]string{"items 1 and 2 are equal"}},
		{"string length counts runes", `{"type":"object","properties":{"s":{"type":"string","maxLength":2}}}`, `{"s":"héé"}`,
			[]string{"at most 2 characters long, got 3"}},
		{"string length ok", `{"type":"object","properties":{"s":{"type":"string","minLength":1,"maxLength":3}}}`, `{"s":"日本"}`, nil},
		{"pattern", `{"type":"object","properties":{"s":{"type":"string","pattern":"^[a-z]+$"}}}`, `{"s":"ABC"}`,
			[]string{`must match pattern "^[a-z]+$"`}},
		{"minimum/maximum", `{"type":"object","properties":{"n":{"type":"number","minimum":1,"maximum":10}}}`, `{"n":11}`,
			[]string{"must be <= 10, got 11"}},
		{"exclusive bounds", `{"type":"object","properties":{"n":{"type":"number","exclusiveMinimum":0}}}`, `{"n":0}`,
			[]string{"must be > 0, got 0"}},
		{"multipleOf decimal", `{"type":"object","properties":{"n":{"type":"number","multipleOf":0.1}}}`, `{"n":0.3}`, nil},
		{"multipleOf bad", `{"type":"object","properties":{"n":{"type":"integer","multipleOf":5}}}`, `{"n":12}`,
			[]string{"multiple of 5"}},
		{"anyOf", `{"type":"object","properties":{"v":{"anyOf":[{"type":"string"},{"type":"integer"}]}}}`, `{"v":true}`,
			[]string{"$.v: must match at least one of the anyOf schemas"}},
		{"oneOf ambiguous", `{"type":"object","properties":{"v":{"oneOf":[{"type":"number"},{"type":"integer"}]}}}`, `{"v":1}`,
			[]string{"matched 2"}},
		{"oneOf ok", `{"type":"object","properties":{"v":{"oneOf":[{"type":"string"},{"type":"integer"}]}}}`, `{"v":1}`, nil},
		{"allOf", `{"type":"object","allOf":[{"required":["a"]},{"required":["b"]}]}`, `{}`,
			[]string{`missing required property "a"`, `missing required property "b"`}},
		{"not", `{"type":"object","properties":{"v":{"not":{"type":"null"}}}}`, `{"v":null}`,
			[]string{"must not match"}},
		{"false schema", `{"type":"object","properties":{"v":false}}`, `{"v":1}`,
			[]string{"$.v: no value is allowed here"}},
		{"annotations are ignored", `{"type":"object","properties":{"d":{"type":"string","format":"date","description":"x","default":"y"}}}`,
			`{"d":"not a date"}`, nil},
		{"several errors are all reported", `{"type":"object","required":["a"],"properties":{"b":{"type":"string"},"c":{"type":"integer"}}}`,
			`{"b":1,"c":"x"}`, []string{`missing required property "a"`, "$.b: expected string", "$.c: expected integer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mustCompile(t, tt.schema).validateInput(json.RawMessage(tt.input))
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got valid", tt.wantErr)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestSchemaValidation_ErrorCountIsBounded(t *testing.T) {
	n := mustCompile(t, `{"type":"object","additionalProperties":false}`)
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 50; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"k` + strings.Repeat("x", i) + `":1`)
	}
	b.WriteString("}")
	err := n.validateInput(json.RawMessage(b.String()))
	if err == nil {
		t.Fatal("want error")
	}
	if got := strings.Count(err.Error(), "unknown property"); got != maxSchemaErrors {
		t.Fatalf("reported %d violations, want %d", got, maxSchemaErrors)
	}
	if !strings.Contains(err.Error(), "further errors omitted") {
		t.Fatalf("error should say it was truncated: %v", err)
	}
}

func TestNewExecutor_RejectsInvalidOrUnsupportedSchemas(t *testing.T) {
	ok := func(ctx context.Context, in json.RawMessage) (any, error) { return nil, nil }
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{"ref", `{"type":"object","properties":{"a":{"$ref":"#/$defs/x"}}}`, `properties.a uses "$ref"`},
		{"if/then", `{"type":"object","if":{},"then":{}}`, `uses "if"`},
		{"patternProperties", `{"type":"object","patternProperties":{"^x":{}}}`, `uses "patternProperties"`},
		{"unknown type", `{"type":"object","properties":{"a":{"type":"strin"}}}`, `unknown type "strin"`},
		{"bad pattern", `{"type":"object","properties":{"a":{"type":"string","pattern":"("}}}`, "invalid regular expression"},
		{"negative minLength", `{"type":"object","properties":{"a":{"minLength":-1}}}`, "non-negative integer"},
		{"non-schema property", `{"type":"object","properties":{"a":"string"}}`, "must be an object or a boolean"},
		{"tuple items", `{"type":"object","properties":{"a":{"items":[{}]}}}`, "tuple-form items"},
		{"zero multipleOf", `{"type":"object","properties":{"a":{"multipleOf":0}}}`, "greater than 0"},
		{"empty anyOf", `{"type":"object","anyOf":[]}`, "non-empty array"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewExecutor(Config{}, NewTool("t", "d", json.RawMessage(tt.schema), ok))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
			if !strings.Contains(err.Error(), `tool "t"`) {
				t.Errorf("error should name the tool: %v", err)
			}
		})
	}
}

func strictTool(called *int) Tool {
	return NewTool("weather", "d", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city":  map[string]any{"type": "string", "minLength": 1},
			"units": map[string]any{"enum": []string{"metric", "imperial"}},
		},
		"required":             []string{"city"},
		"additionalProperties": false,
	}, func(ctx context.Context, in json.RawMessage) (any, error) {
		*called++
		return map[string]string{"ok": "yes"}, nil
	})
}

func TestExecute_InvalidArgumentsNeverReachTheHandler(t *testing.T) {
	called := 0
	ex := mustExecutor(t, Config{}, strictTool(&called))
	results, err := ex.Execute(context.Background(), []Call{
		call("bad", "weather", `{"units":"kelvin","extra":1}`),
		call("good", "weather", `{"city":"Oslo","units":"metric"}`),
		call("empty", "weather", ``),
	})
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("handler ran %d times, want 1 (only the valid call)", called)
	}
	bad := results[0]
	if bad.OK || bad.Error == nil || bad.Error.Code != CodeInvalidArguments {
		t.Fatalf("bad call: %+v", bad)
	}
	for _, want := range []string{`missing required property "city"`, `unknown property "extra"`, `$.units: must be one of ["metric","imperial"]`} {
		if !strings.Contains(bad.Error.Message, want) {
			t.Errorf("message %q missing %q", bad.Error.Message, want)
		}
	}
	if !results[1].OK {
		t.Fatalf("valid call failed: %+v", results[1])
	}
	if results[2].OK || results[2].Error.Code != CodeInvalidArguments {
		t.Fatalf("empty input must be validated as {}: %+v", results[2])
	}

	// The error reaches the model through the provider conversions.
	msgs, err := OpenAIToolMessages(results[:1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msgs[0].Content, `"code":"invalid_arguments"`) {
		t.Fatalf("tool message: %s", msgs[0].Content)
	}
}

func TestExecute_DisableSchemaValidation(t *testing.T) {
	called := 0
	ex := mustExecutor(t, Config{DisableSchemaValidation: true}, strictTool(&called))
	results, err := ex.Execute(context.Background(), []Call{call("x", "weather", `{"nope":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].OK || called != 1 {
		t.Fatalf("handler should run unvalidated input: %+v called=%d", results[0], called)
	}

	ok := func(ctx context.Context, in json.RawMessage) (any, error) { return 1, nil }
	if _, err := NewExecutor(Config{DisableSchemaValidation: true},
		NewTool("r", "d", json.RawMessage(`{"type":"object","properties":{"a":{"$ref":"#/x"}}}`), ok)); err != nil {
		t.Fatalf("unsupported keywords must be accepted when validation is disabled: %v", err)
	}
}
