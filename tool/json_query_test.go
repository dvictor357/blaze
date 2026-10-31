package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func queryJSON(t *testing.T, doc, query, action string) (string, error) {
	t.Helper()
	in := map[string]string{"json": doc, "query": query}
	if action != "" {
		in["action"] = action
	}
	out, err := callTool(t, NewJSONQueryTool(), context.Background(), in)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func TestJSONQuery_SlicesNeverPanicAndFollowPythonSemantics(t *testing.T) {
	const doc = `{"items":[10,20,30]}`
	tests := []struct {
		slice string
		want  string
	}{
		{"[0:2]", `[10,20]`},
		{"[1:]", `[20,30]`},
		{"[:2]", `[10,20]`},
		{"[:]", `[10,20,30]`},
		{"[-2:]", `[20,30]`},
		{"[:-1]", `[10,20]`},
		{"[0:-5]", `[]`}, // end counts from the end, past the beginning
		{"[-5:2]", `[10,20]`},
		{"[-5:-4]", `[]`},
		{"[5:10]", `[]`},
		{"[2:1]", `[]`},
		{"[3:3]", `[]`},
		{"[0:99]", `[10,20,30]`},
		{"[ 1 : 2 ]", `[20]`},
	}
	for _, tt := range tests {
		got, err := queryJSON(t, doc, ".items"+tt.slice, "")
		if err != nil {
			t.Errorf("%s: %v", tt.slice, err)
			continue
		}
		if want := `{"query":".items` + tt.slice + `","result":` + tt.want + `}`; got != want {
			t.Errorf("%s = %s, want %s", tt.slice, got, want)
		}
	}

	for _, bad := range []string{"[a:b]", "[1:2:3]", "[1.5:2]", "[::]"} {
		if _, err := queryJSON(t, doc, ".items"+bad, ""); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := queryJSON(t, `{"s":"text"}`, ".s[0:2]", ""); err == nil {
		t.Error("slicing a string should be an error")
	}
}

func TestJSONQuery_IndexingAndPaths(t *testing.T) {
	const doc = `{"data":{"users":[{"name":"Ann","age":30},{"name":"Bob","age":25}]}}`
	tests := map[string]string{
		".data.users[0].name":  `"Ann"`,
		".data.users[-1].name": `"Bob"`,
		".data.users[*].name":  `["Ann","Bob"]`,
		".data.users.name":     `["Ann","Bob"]`, // implicit map over arrays
		".data.missing_ok":     "",              // error expected
		".data.users[2]":       "",
		".data.users[-3]":      "",
		".data.users[x]":       "",
		".data.users[]":        "",
		".data.users[":         "",
		".data.users]":         "",
		"[":                    "",
		"[]":                   "",
	}
	for query, want := range tests {
		got, err := queryJSON(t, doc, query, "")
		if want == "" {
			if err == nil {
				t.Errorf("%q should fail, got %s", query, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", query, err)
			continue
		}
		if wantFull := `{"query":"` + query + `","result":` + want + `}`; got != wantFull {
			t.Errorf("%q = %s, want %s", query, got, wantFull)
		}
	}
}

func TestJSONQuery_Filters(t *testing.T) {
	const doc = `{"u":[{"name":"Ann","age":30,"tier":"gold"},{"name":"Bob","age":25,"tier":"free"},{"name":"Cy","age":30,"tier":"free"}]}`
	tests := map[string]string{
		`.u[?age>25].name`:       `["Ann","Cy"]`,
		`.u[?age>=30].name`:      `["Ann","Cy"]`, // >= must not parse as > with value "=30"
		`.u[?age<=25].name`:      `["Bob"]`,
		`.u[?age<30].name`:       `["Bob"]`,
		`.u[?tier=="free"].name`: `["Bob","Cy"]`,
		`.u[?tier!="free"].name`: `["Ann"]`,
		`.u[?age==30].name`:      `["Ann","Cy"]`,
		`.u[?name=="Nobody"]`:    `[]`,
		`.u[?nofield==1]`:        `[]`,
	}
	for query, want := range tests {
		got, err := queryJSON(t, doc, query, "")
		if err != nil {
			t.Errorf("%s: %v", query, err)
			continue
		}
		if wantFull := `{"query":` + mustJSON(query) + `,"result":` + want + `}`; got != wantFull {
			t.Errorf("%s = %s, want %s", query, got, wantFull)
		}
	}

	for _, bad := range []string{`.u[?age]`, `.u[?>5]`, `.u[?age~5]`, `.u[?]`} {
		if _, err := queryJSON(t, doc, bad, ""); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := queryJSON(t, `{"o":{}}`, `.o[?a==1]`, ""); err == nil {
		t.Error("filtering a non-array should be an error")
	}
}

func TestJSONQuery_QuotedFilterLiterals(t *testing.T) {
	const doc = `{"items":[{"id":1,"label":"[draft]"},{"id":2,"label":"release.[v1].notes"},{"id":3,"label":"quote\".[draft]"},{"id":4,"label":"C:\\[draft].txt"},{"id":5,"label":"trailing\\"},{"id":6,"label":"slash\\\"[.]"},{"id":7,"label":"]dot.["}]}`
	tests := []struct {
		query string
		want  string
	}{
		{`.items[?label=="[draft]"].id`, `[1]`},
		{`.items[?label=="[draft]"]`, `[{"id":1,"label":"[draft]"}]`},
		{`.items[?label=="release.[v1].notes"].id`, `[2]`},
		{`.items[?label=="quote\".[draft]"].id`, `[3]`},
		{`.items[?label=="C:\\[draft].txt"].id`, `[4]`},
		{`.items[?label=="trailing\\"].id`, `[5]`},
		{`.items[?label=="slash\\\"[.]"].id`, `[6]`},
		{`.items[?label=="]dot.["].id`, `[7]`},
		{`.items[?label=="[missing]"].id`, `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got, err := queryJSON(t, doc, tt.query, "")
			if err != nil {
				t.Fatal(err)
			}
			want := `{"query":` + mustJSON(tt.query) + `,"result":` + tt.want + `}`
			if got != want {
				t.Errorf("query result = %s, want %s", got, want)
			}
		})
	}

	for _, query := range []string{
		`.items[?label=="[draft]]`,
		`.items[?label=="draft\"].id`,
		`.items[?label==draft"].id`,
		`.items[?label=="draft"junk].id`,
		`.items[?label=="bad\q"].id`,
		`.items[?label=="[draft]".id`,
	} {
		t.Run(query, func(t *testing.T) {
			if got, err := queryJSON(t, doc, query, ""); err == nil {
				t.Errorf("malformed quoted filter accepted: %s", got)
			}
		})
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestJSONQuery_ActionsAreDeterministic(t *testing.T) {
	const doc = `{"b":2,"a":1,"c":{"x":[1,[2,3],"1",1,[2,3]]}}`
	tests := []struct {
		query, action, want string
	}{
		{".", "keys", `{"count":3,"keys":["a","b","c"]}`},
		{".c.x", "length", `{"length":5}`},
		{".c", "type", `{"type":"object"}`},
		{".a", "type", `{"type":"number"}`},
		{".c.x", "flatten", `{"result":[1,2,3,"1",1,2,3]}`},
		{".c.x", "unique", `{"result":[1,[2,3],"1"]}`}, // "1" and 1 are different values
		{".", "get", `{"query":".","result":{"a":1,"b":2,"c":{"x":[1,[2,3],"1",1,[2,3]]}}}`},
	}
	for _, tt := range tests {
		for range 3 { // map iteration order must not leak into results
			got, err := queryJSON(t, doc, tt.query, tt.action)
			if err != nil || got != tt.want {
				t.Errorf("%s %s = %s (%v), want %s", tt.action, tt.query, got, err, tt.want)
			}
		}
	}

	if got, err := queryJSON(t, `{"m":{"z":1,"a":2,"k":3}}`, ".m[*]", ""); err != nil || !strings.Contains(got, `[2,3,1]`) {
		t.Errorf("object wildcard must be ordered by key: %s %v", got, err)
	}
	if got, _ := queryJSON(t, `{"s":"héllo"}`, ".s", "length"); got != `{"length":5}` {
		t.Errorf("string length counts characters: %s", got)
	}

	for _, action := range []string{"keys", "flatten", "unique"} {
		if _, err := queryJSON(t, `{"s":"text"}`, ".s", action); err == nil {
			t.Errorf("%s on a string should be an error", action)
		}
	}
	if _, err := queryJSON(t, doc, ".", "explode"); err == nil {
		t.Error("unknown action accepted")
	}
}

func TestJSONQuery_ValidatesItsOwnInput(t *testing.T) {
	tl := NewJSONQueryTool()
	for _, in := range []string{
		`{}`,
		`{"json":"","query":"."}`,
		`{"json":"{not json","query":"."}`,
		`{"json":"{}","query":".a.b"}`,
		`{"json":"{}","query":".","extra":1}`,
		`{"json":42,"query":"."}`,
		`not json`,
	} {
		if _, err := tl.Handler(context.Background(), json.RawMessage(in)); err == nil {
			t.Errorf("input %s accepted", in)
		}
	}
	// A null document is valid JSON; accessing into it is an error, not a panic.
	if _, err := queryJSON(t, `null`, ".a", ""); err == nil {
		t.Error("field access on null should fail")
	}
}
