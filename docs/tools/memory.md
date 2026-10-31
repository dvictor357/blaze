# Memory tool

Bounded, process-local state with explicit ownership, trusted request scopes, and logical TTL expiry. It is not durable storage, a distributed cache, or a reliable queue. Restarting the process loses all data.

## Ownership and trusted scope

```go
store := tool.NewMemoryStore(tool.MemoryOptions{})
memory := tool.NewMemoryTool(store)
ctx := tool.WithMemoryScope(context.Background(), "verified-user/session")
result, err := memory.Handler(ctx, json.RawMessage(`{"action":"set","key":"preference","value":"dark"}`))
```

This snippet uses `context`, `encoding/json`, and `github.com/dvictor357/blaze/tool`. The scope string must come from trusted server-side authentication/session code. Never trust a caller-supplied namespace, query parameter, or JSON field. Every tool invocation requires a nonempty scope; missing scope returns `ErrMemoryScopeRequired`.

Separate stores share nothing. Passing nil to `NewMemoryTool` allocates a private default store. Sharing a store intentionally shares only data within the same scope. `get`, `keys`, `list`, `delete`, and `clear` cannot cross scopes. `store.Forget(scope)` lets trusted application code end a scope's lifecycle.

The [example](../../examples/main.go) derives scope after verifying a bearer token. A multi-user service must map each verified identity/session to its own scope.

## Actions

Each JSON object below is a tool call's `input`, not a complete HTTP invocation.

| Action | Behavior |
|---|---|
| `set` | Store JSON `value` under `key`; replace existing value and TTL |
| `get` | Retrieve a scalar value; `found:false` for missing/expired keys |
| `delete` | Remove the key |
| `keys` | Sorted live keys in the current scope |
| `list` | Snapshot all live values/lists in the current scope |
| `clear` | Remove only the current scope's data |
| `incr` / `decr` | Add/subtract integer `value` (default one); missing key starts at zero |
| `append` | Append a JSON item to a list, creating it when absent |
| `pop` | Remove the last list item; return `empty:true` when absent |
| `lrange` | Snapshot an inclusive list range |
| `llen` | List length and existence |

```json
{"action":"set","key":"preference","value":"dark","ttl":3600}
```

```json
{"action":"incr","key":"requests"}
```

```json
{"action":"append","key":"history","value":{"event":"search"}}
```

```json
{"action":"lrange","key":"history","start":0,"end":0}
```

Explicit `end:0` selects the first element; omitted `end` means the last element. Negative indices count from the end; ranges are clamped and may be empty. Scalars and lists share one keyspace. Scalar operations on a list, and list operations on a scalar, report errors; `set` deliberately replaces an existing key. There are no `lpop`/`rpop` aliases.

Counters are signed 64-bit integers. Non-integer stored values, fractional increments, and arithmetic overflow return errors instead of resetting or wrapping a counter.

## TTL and concurrency

TTL is in whole seconds. Omitted/zero TTL on `set` means no expiry. For `append`/`incr`/`decr`, omitted TTL preserves a live key's expiry; an explicit zero removes it, and a positive value sets a new deadline. Expired keys behave as absent across all operations, including enumeration and counters. Invalid negative/over-cap TTLs fail without changing data.

Expiry is logical: entries become invisible at the deadline and their space is reclaimed by later scope operations or a capacity sweep. There is no background expiration goroutine. Returned scalar/list snapshots own their bytes; callers can serialize or mutate them without racing store updates. Store operations synchronize access, but the store remains process-local and uses a shared mutex across scopes.

## Default quotas

| Limit | Default |
|---|---:|
| Active scopes | 1,024 |
| Keys per scope | 256 |
| JSON bytes per scalar/list item | 16 KiB |
| Key/value bytes per scope | 256 KiB |
| Items per list | 1,024 |
| Maximum explicit TTL | 24 hours |

Configure through `MemoryOptions`; nonpositive option fields select defaults. Keys and scope strings are also limited to 256 bytes. Quota failures wrap `ErrMemoryQuota`. Byte quotas count stored payload/key bytes, not total Go heap overhead. Use application-level limits and external storage when durability, large datasets, or cross-process coordination are required.
