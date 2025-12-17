# DateTime Tool

Time and timezone operations for AI agents.

## Actions

### `now` — Get Current Time

```json
{"action": "now", "timezone": "America/New_York"}
```

**Response:**
```json
{
  "time": "2024-12-17T09:45:00-05:00",
  "timezone": "America/New_York",
  "unix": 1734444300
}
```

---

### `parse` — Parse a Date

```json
{"action": "parse", "date": "2024-12-25"}
```

Supports various formats: ISO 8601, RFC822, Unix timestamps, and more.

---

### `diff` — Calculate Difference

```json
{"action": "diff", "date": "2024-01-01", "date2": "2024-12-31"}
```

**Response:**
```json
{
  "days": 365,
  "hours": 8760,
  "human": "365 days"
}
```

---

### `add` — Add Duration

```json
{"action": "add", "date": "2024-01-01", "duration": "30d"}
```

**Duration formats:**
- `30s` — 30 seconds
- `5m` — 5 minutes
- `2h` — 2 hours
- `7d` — 7 days
- `1w` — 1 week

---

## Capabilities

| Feature | Description |
|---------|-------------|
| Current time | Any timezone |
| Parse dates | Various formats |
| Time differences | Between two dates |
| Add/subtract | Durations |
| Format dates | ISO, RFC822, Unix, human-readable |

---

## Usage

```go
import "github.com/dvictor357/blaze/tool"

datetimeTool := tool.NewDateTimeTool()
```

---

## See Also

- [Web Tools](web.md)
- [JSON Query Tool](json-query.md)
- [Memory Tool](memory.md)
