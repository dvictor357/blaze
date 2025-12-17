# Memory Tool

In-memory key-value store for AI agents. Thread-safe with TTL support.

## Actions

### `set` — Store a Value

```json
{"action": "set", "key": "user_preference", "value": "dark_mode", "ttl": 3600}
```

TTL is optional (seconds). Without TTL, values persist until server restart.

---

### `get` — Retrieve a Value

```json
{"action": "get", "key": "user_preference"}
```

**Response:**
```json
{
  "key": "user_preference",
  "value": "dark_mode",
  "exists": true
}
```

---

### `delete` — Remove a Value

```json
{"action": "delete", "key": "user_preference"}
```

---

### `incr` / `decr` — Counter Operations

```json
{"action": "incr", "key": "request_count"}
{"action": "decr", "key": "request_count"}
```

Creates key with value 0 if it doesn't exist.

---

### `append` — Add to List

```json
{"action": "append", "key": "history", "value": "searched: golang"}
```

---

### `lrange` — Get List Range

```json
{"action": "lrange", "key": "history", "start": 0, "end": -1}
```

Use `-1` for end to get all items.

---

### `lpop` / `rpop` — Pop from List

```json
{"action": "lpop", "key": "queue"}  // Pop from left
{"action": "rpop", "key": "queue"}  // Pop from right
```

---

## Features

| Feature | Description |
|---------|-------------|
| Key-value storage | Simple get/set |
| TTL support | Auto-expiring keys |
| Counters | Atomic incr/decr |
| Lists | append, pop, range |
| Thread-safe | Concurrent access |

---

## Usage

```go
import "github.com/dvictor357/blaze/tool"

memoryTool := tool.NewMemoryTool()
```

---

## Use Cases

- **Session state**: Store user preferences across tool calls
- **Counters**: Track request counts, rate limiting
- **Queues**: Task queues with lpop/rpop
- **Caching**: Store expensive computation results
- **History**: Maintain conversation context

---

## See Also

- [Web Tools](web.md)
- [DateTime Tool](datetime.md)
- [JSON Query Tool](json-query.md)
