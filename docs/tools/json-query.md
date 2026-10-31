# JSON query tool

`tool.NewJSONQueryTool()` provides a small path/filter language over a JSON string. It is not a complete jq implementation. It is a context-aware `adapter.Tool` for an executor; the executor's input/output budgets also apply.

## Input

```json
{"json":"{\"users\":[{\"name\":\"Alice\",\"age\":30},{\"name\":\"Bob\",\"age\":25}]}","query":".users[?age>25].name"}
```

`json` is a JSON-encoded string containing the document. `query` is a path; an empty query or `.` selects the document. `action` defaults to `get`. Unknown input fields and malformed documents/paths return tool errors.

| Syntax | Meaning |
|---|---|
| `.data.name` | Object fields |
| `.users[0]` | Array index; negative indices count from the end |
| `.items[0:5]` | Slice with exclusive end |
| `.users[*].email` | Wildcard/field projection |
| `.users[?age>=18]` | Simple field comparison filter |

Slice bounds are clamped, negative bounds count from the end, and reversed/fully out-of-range slices become empty. Malformed slices return errors instead of panicking. Filters support `==`, `!=`, `>`, `<`, `>=`, and `<=`; this is not a general expression parser and does not support arbitrary jq programs or quoted-key syntax.

## Actions and outputs

| Action | Output object |
|---|---|
| `get` | `result` and `query` |
| `keys` | `keys` and `count` for an object |
| `length` | `length` |
| `type` | `type` |
| `flatten` | `result` with nested arrays flattened |
| `unique` | `result` with duplicate array values removed |

For example, `{"json":"[1,2,3]","query":".","action":"length"}` returns an output object with `length:3`, inside the executor's correlated result envelope.

The document currently uses Go's `encoding/json` default number representation. Do not use it for lossless arithmetic or preservation of arbitrary large integers; provider call-conversion helpers preserve raw JSON separately. Validate application requirements before using this utility on financial or identifier data.
