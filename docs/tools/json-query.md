# JSON Query Tool

jq-like JSON querying for AI agents.

## Basic Usage

```json
{
  "json": "{\"users\": [{\"name\": \"Alice\", \"age\": 30}, {\"name\": \"Bob\", \"age\": 25}]}",
  "query": ".users[?age>25].name"
}
```

**Response:**
```json
{
  "result": ["Alice"]
}
```

---

## Query Syntax

| Syntax | Description | Example |
|--------|-------------|---------|
| `.field` | Access field | `.data.name` |
| `[n]` | Array index | `.users[0]` |
| `[n:m]` | Array slice | `.items[0:5]` |
| `[*]` | Wildcard | `.users[*].email` |
| `[?cond]` | Filter | `[?status=="active"]` |

---

## Actions

| Action | Description |
|--------|-------------|
| `get` | Extract value (default) |
| `keys` | List object keys |
| `length` | Count items |
| `type` | Get JSON type |
| `flatten` | Flatten nested arrays |
| `unique` | Deduplicate array |

### Examples

**Get keys:**
```json
{"json": "{\"a\": 1, \"b\": 2}", "action": "keys"}
// Returns: ["a", "b"]
```

**Count items:**
```json
{"json": "[1, 2, 3, 4, 5]", "action": "length"}
// Returns: 5
```

---

## Usage

```go
import "github.com/dvictor357/blaze/tool"

jsonQueryTool := tool.NewJSONQueryTool()
```

---

## See Also

- [Web Tools](web.md)
- [DateTime Tool](datetime.md)
- [Memory Tool](memory.md)
