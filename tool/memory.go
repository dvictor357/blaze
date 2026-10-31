package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dvictor357/blaze/adapter"
)

// Errors returned by the memory tool.
var (
	// ErrMemoryScopeRequired is returned when a memory call carries no scope.
	ErrMemoryScopeRequired = errors.New("memory scope required: the server must attach one with tool.WithMemoryScope after authenticating the caller")
	// ErrMemoryQuota is returned (wrapped) when a store limit would be exceeded.
	ErrMemoryQuota = errors.New("memory quota exceeded")
)

type memoryScopeKey struct{}

// WithMemoryScope returns a context that carries the trusted memory scope of
// the caller (for example a tenant or user ID). It must be set only by
// server-side code, such as authentication middleware, from a verified
// identity, never from request JSON: the memory tool has no namespace
// parameter and cannot read or enumerate another scope's keys.
func WithMemoryScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, memoryScopeKey{}, scope)
}

// MemoryScopeFrom returns the scope attached by WithMemoryScope.
func MemoryScopeFrom(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(memoryScopeKey{}).(string)
	return s, ok && s != ""
}

// Defaults applied by NewMemoryStore to unset MemoryOptions fields.
const (
	DefaultMemoryMaxScopes     = 1024
	DefaultMemoryMaxKeys       = 256
	DefaultMemoryMaxValueBytes = 16 << 10
	DefaultMemoryMaxScopeBytes = 256 << 10
	DefaultMemoryMaxListLen    = 1024
	DefaultMemoryMaxTTL        = 24 * time.Hour

	maxMemoryKeyBytes   = 256
	maxMemoryScopeBytes = 256
)

// MemoryOptions bounds a MemoryStore. Zero fields take the defaults above.
type MemoryOptions struct {
	// MaxScopes is the number of scopes with live data (default 1024).
	MaxScopes int
	// MaxKeysPerScope bounds scalar and list keys together (default 256).
	MaxKeysPerScope int
	// MaxValueBytes bounds one stored value or list item, as encoded JSON
	// (default 16 KiB).
	MaxValueBytes int
	// MaxScopeBytes bounds the keys and values of one scope (default 256 KiB).
	MaxScopeBytes int
	// MaxListLen bounds a list (default 1024).
	MaxListLen int
	// MaxTTL is the largest accepted ttl (default 24h). Entries without a ttl
	// live until the process exits or the scope is cleared or forgotten.
	MaxTTL time.Duration
	// Now is the clock (default time.Now); tests inject their own.
	Now func() time.Time
}

// MemoryStore is a bounded, process-local, in-memory store partitioned by
// scope. It is explicitly owned: create one with NewMemoryStore and give it to
// NewMemoryTool; stores never share data unless the same store is deliberately
// passed to several tools. Data is lost on restart. Expiry is logical: expired
// entries are invisible immediately and their space is reclaimed by the next
// operation on the scope (or when the scope table is full), with no background
// goroutine.
type MemoryStore struct {
	mu     sync.Mutex
	opts   MemoryOptions
	scopes map[string]*memoryPartition
}

type memoryPartition struct {
	entries map[string]*memoryEntry
	bytes   int // sum of len(key)+entry.size
}

type memoryEntry struct {
	list    bool
	scalar  json.RawMessage   // encoded JSON; set when !list
	items   []json.RawMessage // encoded JSON items; set when list
	created time.Time
	expires time.Time // zero = no expiry
	size    int       // payload bytes, excluding the key
}

// NewMemoryStore creates an empty store.
func NewMemoryStore(opts MemoryOptions) *MemoryStore {
	if opts.MaxScopes <= 0 {
		opts.MaxScopes = DefaultMemoryMaxScopes
	}
	if opts.MaxKeysPerScope <= 0 {
		opts.MaxKeysPerScope = DefaultMemoryMaxKeys
	}
	if opts.MaxValueBytes <= 0 {
		opts.MaxValueBytes = DefaultMemoryMaxValueBytes
	}
	if opts.MaxScopeBytes <= 0 {
		opts.MaxScopeBytes = DefaultMemoryMaxScopeBytes
	}
	if opts.MaxListLen <= 0 {
		opts.MaxListLen = DefaultMemoryMaxListLen
	}
	if opts.MaxTTL <= 0 {
		opts.MaxTTL = DefaultMemoryMaxTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &MemoryStore{opts: opts, scopes: make(map[string]*memoryPartition)}
}

// Forget drops everything stored for a scope, for example when a session ends.
func (s *MemoryStore) Forget(scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.scopes, scope)
}

// NewMemoryTool creates the "memory" tool on top of store. A nil store gets a
// new private store with default options (never a shared global one).
//
// Every call must run in a context carrying a scope (see WithMemoryScope);
// without one the tool fails with ErrMemoryScopeRequired. Scalars and lists
// share one keyspace per scope: a key holds either a JSON value or a list, and
// using it as the other kind is an error. ttl applies to either kind.
//
// Cancellation is cooperative: calls check the context before waiting for the
// store lock and again after acquiring it, before touching stored data. Waiting
// for the mutex itself is not interruptible; a canceled waiter returns once
// admitted without sweeping or running its action. An action already admitted
// runs to completion under the lock.
func NewMemoryTool(store *MemoryStore) adapter.Tool {
	if store == nil {
		store = NewMemoryStore(MemoryOptions{})
	}
	return adapter.NewTool(
		"memory",
		"Store and retrieve small pieces of data across tool calls: key-value with optional TTL, counters and lists. Data is private to the calling session or tenant, bounded in size, kept in process memory only and lost when the server restarts.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"set", "get", "delete", "list", "keys", "clear", "incr", "decr", "append", "pop", "lrange", "llen"},
					"description": "Action: 'set/get/delete' for key-value, 'incr/decr' for integer counters, 'append/pop/lrange/llen' for lists, 'keys' to list all keys, 'list' to dump all, 'clear' to reset your data",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Key name (required except for keys, list and clear)",
				},
				"value": map[string]any{
					"description": "Value to store (any JSON) for set/append, or an integer amount for incr/decr (default 1)",
				},
				"ttl": map[string]any{
					"type":        "integer",
					"description": "Time-to-live in seconds for set, incr, decr and append (0 = no expiry)",
				},
				"start": map[string]any{
					"type":        "integer",
					"description": "Start index for lrange (default: 0; negative counts from the end)",
				},
				"end": map[string]any{
					"type":        "integer",
					"description": "Inclusive end index for lrange (default: -1 = last item)",
				},
			},
			"required": []string{"action"},
		},
		func(ctx context.Context, input json.RawMessage) (any, error) {
			scope, ok := MemoryScopeFrom(ctx)
			if !ok {
				return nil, ErrMemoryScopeRequired
			}
			if len(scope) > maxMemoryScopeBytes {
				return nil, errors.New("memory scope is too long")
			}

			var in memoryInput
			if err := decodeInput(input, &in); err != nil {
				return nil, err
			}
			if err := store.validate(&in); err != nil {
				return nil, err
			}
			return store.exec(ctx, scope, &in)
		},
	)
}

type memoryInput struct {
	Action string          `json:"action"`
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
	TTL    *int64          `json:"ttl"`
	Start  *int            `json:"start"`
	End    *int            `json:"end"`
}

// validate checks everything that does not need the lock.
func (s *MemoryStore) validate(in *memoryInput) error {
	switch in.Action {
	case "keys", "list", "clear":
		return nil
	case "set", "get", "delete", "incr", "decr", "append", "pop", "lrange", "llen":
	default:
		return fmt.Errorf("unknown action: %q", in.Action)
	}
	if in.Key == "" {
		return fmt.Errorf("key is required for %s", in.Action)
	}
	if len(in.Key) > maxMemoryKeyBytes || !utf8.ValidString(in.Key) {
		return fmt.Errorf("key must be valid UTF-8 of at most %d bytes", maxMemoryKeyBytes)
	}
	switch in.Action {
	case "set", "append":
		if len(in.Value) == 0 {
			return fmt.Errorf("value is required for %s", in.Action)
		}
		if len(in.Value) > s.opts.MaxValueBytes {
			return fmt.Errorf("%w: value of %d bytes exceeds %d", ErrMemoryQuota, len(in.Value), s.opts.MaxValueBytes)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, in.Value); err != nil {
			return fmt.Errorf("invalid value: %w", err)
		}
		in.Value = compact.Bytes()
	case "incr", "decr":
		if len(in.Value) > 0 {
			if bytes.Equal(bytes.TrimSpace(in.Value), []byte("null")) {
				return fmt.Errorf("%s amount must be an integer", in.Action)
			}
			var n int64
			if err := json.Unmarshal(in.Value, &n); err != nil {
				return fmt.Errorf("%s amount must be an integer", in.Action)
			}
		}
	}
	return nil
}

func (s *MemoryStore) expiry(ttl *int64, now time.Time) (time.Time, error) {
	if ttl == nil || *ttl == 0 {
		return time.Time{}, nil
	}
	if *ttl < 0 {
		return time.Time{}, errors.New("ttl must not be negative")
	}
	if *ttl > int64(s.opts.MaxTTL/time.Second) {
		return time.Time{}, fmt.Errorf("ttl must be at most %d seconds", int64(s.opts.MaxTTL/time.Second))
	}
	return now.Add(time.Duration(*ttl) * time.Second), nil
}

// exec runs one action under the store lock. Reads also take the exclusive
// lock: they sweep expired entries, and holding one lock for check-and-delete
// means a reader can never delete a value another call just wrote.
func (s *MemoryStore) exec(ctx context.Context, scope string, in *memoryInput) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	now := s.opts.Now()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := s.scopes[scope]
	fresh := p == nil
	if fresh {
		p = &memoryPartition{entries: make(map[string]*memoryEntry)}
	} else {
		p.sweep(now)
	}

	out, err := s.run(p, in, now)

	switch {
	case fresh && err == nil && len(p.entries) > 0:
		if len(s.scopes) >= s.opts.MaxScopes {
			s.sweepScopes(now)
		}
		if len(s.scopes) >= s.opts.MaxScopes {
			return nil, fmt.Errorf("%w: too many active scopes", ErrMemoryQuota)
		}
		s.scopes[scope] = p
	case !fresh && len(p.entries) == 0:
		delete(s.scopes, scope)
	}
	return out, err
}

func (s *MemoryStore) sweepScopes(now time.Time) {
	for name, p := range s.scopes {
		p.sweep(now)
		if len(p.entries) == 0 {
			delete(s.scopes, name)
		}
	}
}

func (p *memoryPartition) sweep(now time.Time) {
	for k, e := range p.entries {
		if e.expired(now) {
			p.drop(k)
		}
	}
}

func (e *memoryEntry) expired(now time.Time) bool {
	return !e.expires.IsZero() && !now.Before(e.expires)
}

func (p *memoryPartition) drop(key string) {
	if e, ok := p.entries[key]; ok {
		p.bytes -= len(key) + e.size
		delete(p.entries, key)
	}
}

// reserve accounts delta bytes (and one more key if newKey) or fails without
// changing anything.
func (p *memoryPartition) reserve(o MemoryOptions, delta int, newKey bool) error {
	if newKey && len(p.entries) >= o.MaxKeysPerScope {
		return fmt.Errorf("%w: at most %d keys per scope", ErrMemoryQuota, o.MaxKeysPerScope)
	}
	if delta > 0 && p.bytes+delta > o.MaxScopeBytes {
		return fmt.Errorf("%w: scope data would exceed %d bytes", ErrMemoryQuota, o.MaxScopeBytes)
	}
	p.bytes += delta
	return nil
}

func cloneRaw(r json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), r...)
}

func cloneItems(items []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(items))
	for i, it := range items {
		out[i] = cloneRaw(it)
	}
	return out
}

var errWrongKind = errors.New("key holds a different kind of value (scalar vs list)")

func (s *MemoryStore) run(p *memoryPartition, in *memoryInput, now time.Time) (map[string]any, error) {
	key := in.Key
	switch in.Action {
	case "set":
		exp, err := s.expiry(in.TTL, now)
		if err != nil {
			return nil, err
		}
		old := p.entries[key]
		delta := len(key) + len(in.Value)
		if old != nil {
			delta -= len(key) + old.size
		}
		if err := p.reserve(s.opts, delta, old == nil); err != nil {
			return nil, err
		}
		p.entries[key] = &memoryEntry{scalar: cloneRaw(in.Value), size: len(in.Value), created: now, expires: exp}
		ttl := int64(0)
		if in.TTL != nil {
			ttl = *in.TTL
		}
		return map[string]any{"success": true, "key": key, "ttl": ttl}, nil

	case "get":
		e := p.entries[key]
		if e == nil {
			return map[string]any{"found": false, "key": key}, nil
		}
		if e.list {
			return nil, fmt.Errorf("%w: use lrange for lists", errWrongKind)
		}
		res := map[string]any{
			"found":      true,
			"key":        key,
			"value":      cloneRaw(e.scalar),
			"created_at": e.created.Format(time.RFC3339),
		}
		if !e.expires.IsZero() {
			res["expires_at"] = e.expires.Format(time.RFC3339)
			res["ttl_remaining"] = int64(math.Ceil(e.expires.Sub(now).Seconds()))
		}
		return res, nil

	case "delete":
		_, existed := p.entries[key]
		p.drop(key)
		return map[string]any{"success": true, "key": key, "existed": existed}, nil

	case "keys":
		keys := make([]string, 0, len(p.entries))
		for k := range p.entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return map[string]any{"keys": keys, "count": len(keys)}, nil

	case "list":
		data := make(map[string]any, len(p.entries))
		for k, e := range p.entries {
			if e.list {
				data[k] = cloneItems(e.items)
			} else {
				data[k] = cloneRaw(e.scalar)
			}
		}
		return map[string]any{"data": data, "count": len(data)}, nil

	case "clear":
		count := len(p.entries)
		p.entries = make(map[string]*memoryEntry)
		p.bytes = 0
		return map[string]any{"success": true, "cleared": count}, nil

	case "incr", "decr":
		return s.counter(p, in, now)

	case "append":
		e := p.entries[key]
		if e != nil && !e.list {
			return nil, fmt.Errorf("%w: use set/get for scalars", errWrongKind)
		}
		exp, err := s.expiry(in.TTL, now)
		if err != nil {
			return nil, err
		}
		if e == nil {
			if err := p.reserve(s.opts, len(key)+len(in.Value), true); err != nil {
				return nil, err
			}
			e = &memoryEntry{list: true, created: now}
			p.entries[key] = e
		} else {
			if len(e.items) >= s.opts.MaxListLen {
				return nil, fmt.Errorf("%w: lists hold at most %d items", ErrMemoryQuota, s.opts.MaxListLen)
			}
			if err := p.reserve(s.opts, len(in.Value), false); err != nil {
				return nil, err
			}
		}
		e.items = append(e.items, cloneRaw(in.Value))
		e.size += len(in.Value)
		if in.TTL != nil {
			e.expires = exp
		}
		return map[string]any{"key": key, "length": len(e.items)}, nil

	case "pop":
		e := p.entries[key]
		if e == nil || (e.list && len(e.items) == 0) {
			return map[string]any{"key": key, "empty": true}, nil
		}
		if !e.list {
			return nil, fmt.Errorf("%w: use get for scalars", errWrongKind)
		}
		last := len(e.items) - 1
		item := e.items[last]
		e.items[last] = nil
		e.items = e.items[:last]
		e.size -= len(item)
		p.bytes -= len(item)
		length := len(e.items)
		if length == 0 {
			p.drop(key)
		}
		return map[string]any{"key": key, "value": item, "length": length}, nil

	case "lrange":
		e := p.entries[key]
		if e == nil {
			return map[string]any{"key": key, "items": []json.RawMessage{}, "length": 0, "total": 0}, nil
		}
		if !e.list {
			return nil, fmt.Errorf("%w: use get for scalars", errWrongKind)
		}
		n := len(e.items)
		start, end := 0, -1
		if in.Start != nil {
			start = *in.Start
		}
		if in.End != nil {
			end = *in.End
		}
		if start < 0 {
			start += n
		}
		if end < 0 {
			end += n
		}
		start = max(start, 0)
		end = min(end, n-1)
		items := []json.RawMessage{}
		if start <= end {
			items = cloneItems(e.items[start : end+1])
		}
		return map[string]any{"key": key, "items": items, "length": len(items), "total": n}, nil

	case "llen":
		e := p.entries[key]
		if e == nil {
			return map[string]any{"key": key, "length": 0, "exists": false}, nil
		}
		if !e.list {
			return nil, fmt.Errorf("%w: use get for scalars", errWrongKind)
		}
		return map[string]any{"key": key, "length": len(e.items), "exists": true}, nil
	}
	return nil, fmt.Errorf("unknown action: %q", in.Action)
}

// counter implements incr and decr. A missing or expired key starts at 0; a
// live ttl is preserved unless the call passes an explicit ttl; a value that
// is not an integer is an error, never silently reset.
func (s *MemoryStore) counter(p *memoryPartition, in *memoryInput, now time.Time) (map[string]any, error) {
	key := in.Key
	amount := int64(1)
	if len(in.Value) > 0 {
		if err := json.Unmarshal(in.Value, &amount); err != nil {
			return nil, fmt.Errorf("%s amount must be an integer", in.Action)
		}
	}
	if in.Action == "decr" {
		if amount == math.MinInt64 {
			return nil, errors.New("amount out of range")
		}
		amount = -amount
	}

	old := p.entries[key]
	current := int64(0)
	if old != nil {
		if old.list {
			return nil, fmt.Errorf("%w: key holds a list", errWrongKind)
		}
		if bytes.Equal(bytes.TrimSpace(old.scalar), []byte("null")) {
			return nil, errors.New("value is not an integer")
		}
		if err := json.Unmarshal(old.scalar, &current); err != nil {
			return nil, errors.New("value is not an integer")
		}
	}
	if (amount > 0 && current > math.MaxInt64-amount) || (amount < 0 && current < math.MinInt64-amount) {
		return nil, errors.New("counter overflow")
	}
	next := current + amount
	encoded := strconv.FormatInt(next, 10)
	if len(encoded) > s.opts.MaxValueBytes {
		return nil, fmt.Errorf("%w: value of %d bytes exceeds %d", ErrMemoryQuota, len(encoded), s.opts.MaxValueBytes)
	}
	raw := json.RawMessage(encoded)

	exp := time.Time{}
	created := now
	if old != nil {
		exp, created = old.expires, old.created
	}
	if in.TTL != nil {
		var err error
		if exp, err = s.expiry(in.TTL, now); err != nil {
			return nil, err
		}
	}

	delta := len(key) + len(raw)
	if old != nil {
		delta -= len(key) + old.size
	}
	if err := p.reserve(s.opts, delta, old == nil); err != nil {
		return nil, err
	}
	p.entries[key] = &memoryEntry{scalar: raw, size: len(raw), created: created, expires: exp}
	return map[string]any{"key": key, "previous": current, "current": next}, nil
}
