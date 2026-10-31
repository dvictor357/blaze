package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dvictor357/blaze/adapter"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *testClock {
	return &testClock{now: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newMemory(t *testing.T, opts MemoryOptions) (adapter.Tool, *testClock) {
	t.Helper()
	clock := newClock()
	opts.Now = clock.Now
	return NewMemoryTool(NewMemoryStore(opts)), clock
}

// mem runs one memory action in a scope and returns the result as generic JSON.
func mem(tl adapter.Tool, scope string, in map[string]any) (map[string]any, error) {
	ctx := context.Background()
	if scope != "" {
		ctx = WithMemoryScope(ctx, scope)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	out, err := tl.Handler(ctx, raw)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func mustMem(t *testing.T, tl adapter.Tool, scope string, in map[string]any) map[string]any {
	t.Helper()
	out, err := mem(tl, scope, in)
	if err != nil {
		t.Fatalf("memory %v: %v", in, err)
	}
	return out
}

func found(t *testing.T, tl adapter.Tool, scope, key string) bool {
	t.Helper()
	return mustMem(t, tl, scope, map[string]any{"action": "get", "key": key})["found"] == true
}

// memoryObservedContext reports the first cancellation check after taking its
// snapshot, so a test can cancel a caller known to have passed the pre-lock check.
type memoryObservedContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *memoryObservedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestMemory_CanceledCallsDoNotMutate(t *testing.T) {
	clock := newClock()
	clockCalls := 0
	store := NewMemoryStore(MemoryOptions{Now: func() time.Time {
		clockCalls++
		return clock.Now()
	}})
	tl := NewMemoryTool(store)
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "n", "value": 9})
	mustMem(t, tl, "s", map[string]any{"action": "append", "key": "log", "value": 1})
	clockCalls = 0

	ctx, cancel := context.WithCancel(WithMemoryScope(context.Background(), "s"))
	cancel()
	for _, raw := range []string{
		`{"action":"set","key":"n","value":0}`,
		`{"action":"set","key":"new","value":0}`,
		`{"action":"delete","key":"n"}`,
		`{"action":"clear"}`,
		`{"action":"incr","key":"n"}`,
		`{"action":"decr","key":"n"}`,
		`{"action":"append","key":"log","value":2}`,
		`{"action":"pop","key":"log"}`,
		`{"action":"get","key":"n"}`,
		`{"action":"keys"}`,
		`{"action":"list"}`,
		`{"action":"lrange","key":"log"}`,
		`{"action":"llen","key":"log"}`,
	} {
		if _, err := tl.Handler(ctx, json.RawMessage(raw)); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: error = %v, want context.Canceled", raw, err)
		}
	}
	if clockCalls != 0 {
		t.Errorf("canceled calls reached expiry processing %d times", clockCalls)
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "n"}); got["value"] != float64(9) {
		t.Errorf("canceled calls changed scalar: %v", got)
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "lrange", "key": "log"}); !reflect.DeepEqual(got["items"], []any{float64(1)}) {
		t.Errorf("canceled calls changed list: %v", got)
	}
	if found(t, tl, "s", "new") {
		t.Error("canceled set created a key")
	}
}

func TestMemory_CanceledLockWaiterDoesNotMutate(t *testing.T) {
	clockCalls := 0
	store := NewMemoryStore(MemoryOptions{Now: func() time.Time {
		clockCalls++
		return time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	}})
	tl := NewMemoryTool(store)
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "n", "value": 9})
	clockCalls = 0
	base, cancel := context.WithCancel(WithMemoryScope(context.Background(), "s"))
	defer cancel()
	ctx := &memoryObservedContext{Context: base, checked: make(chan struct{})}
	done := make(chan error, 1)

	// The waiter snapshots a live context before trying the held mutex. Cancel
	// before admitting it; admission must recheck without even invoking the clock.
	store.mu.Lock()
	go func() {
		_, err := tl.Handler(ctx, json.RawMessage(`{"action":"incr","key":"n"}`))
		done <- err
	}()
	<-ctx.checked
	cancel()
	store.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("canceled waiter: error = %v, want context.Canceled", err)
	}
	if clockCalls != 0 {
		t.Errorf("canceled waiter reached expiry processing %d times", clockCalls)
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "n"}); got["value"] != float64(9) {
		t.Errorf("canceled waiter changed counter: %v", got)
	}
}

func TestMemory_CancellationFromClockDoesNotSweepOrMutate(t *testing.T) {
	clock := newClock()
	var cancelOnClock context.CancelFunc
	store := NewMemoryStore(MemoryOptions{Now: func() time.Time {
		if cancelOnClock != nil {
			cancelOnClock()
		}
		return clock.Now()
	}})
	tl := NewMemoryTool(store)
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "n", "value": 9, "ttl": 1})
	clock.Advance(time.Second)
	ctx, cancel := context.WithCancel(WithMemoryScope(context.Background(), "s"))
	defer cancel()
	cancelOnClock = cancel
	if _, err := tl.Handler(ctx, json.RawMessage(`{"action":"set","key":"n","value":0}`)); !errors.Is(err, context.Canceled) {
		t.Errorf("clock-canceled call: error = %v, want context.Canceled", err)
	}
	cancelOnClock = nil
	// Restore the clock to expose whether the canceled call swept the old entry.
	clock.Advance(-time.Second)
	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "n"}); got["found"] != true || got["value"] != float64(9) {
		t.Errorf("clock-canceled call swept or replaced value: %v", got)
	}
}

func TestMemory_RequiresATrustedScope(t *testing.T) {
	tl, _ := newMemory(t, MemoryOptions{})

	for _, in := range []map[string]any{
		{"action": "set", "key": "k", "value": 1},
		{"action": "get", "key": "k"},
		{"action": "keys"},
		{"action": "list"},
		{"action": "clear"},
	} {
		if _, err := mem(tl, "", in); !errors.Is(err, ErrMemoryScopeRequired) {
			t.Errorf("%v without a scope: error = %v, want ErrMemoryScopeRequired", in, err)
		}
	}

	// An empty scope is no scope.
	raw, _ := json.Marshal(map[string]any{"action": "keys"})
	if _, err := tl.Handler(WithMemoryScope(context.Background(), ""), raw); !errors.Is(err, ErrMemoryScopeRequired) {
		t.Errorf("empty scope: error = %v", err)
	}

	// The caller cannot pick a scope (or namespace) in the call itself.
	for _, field := range []string{"scope", "namespace", "tenant", "session"} {
		in := map[string]any{"action": "get", "key": "k", field: "victim"}
		if _, err := mem(tl, "alice", in); err == nil {
			t.Errorf("input field %q was accepted", field)
		}
	}
}

func TestMemory_ScopesCannotSeeOrAffectEachOther(t *testing.T) {
	tl, _ := newMemory(t, MemoryOptions{})

	mustMem(t, tl, "alice", map[string]any{"action": "set", "key": "secret", "value": "alice-data"})
	mustMem(t, tl, "alice", map[string]any{"action": "append", "key": "log", "value": "entry"})
	mustMem(t, tl, "alice", map[string]any{"action": "incr", "key": "n"})

	if found(t, tl, "bob", "secret") {
		t.Error("bob read alice's key")
	}
	if got := mustMem(t, tl, "bob", map[string]any{"action": "keys"}); got["count"] != float64(0) {
		t.Errorf("bob enumerates keys: %v", got)
	}
	if got := mustMem(t, tl, "bob", map[string]any{"action": "list"}); got["count"] != float64(0) {
		t.Errorf("bob dumps data: %v", got)
	}
	if got := mustMem(t, tl, "bob", map[string]any{"action": "llen", "key": "log"}); got["exists"] != false {
		t.Errorf("bob sees alice's list: %v", got)
	}

	// Bob writing and clearing his data leaves Alice's untouched, even with the same key.
	mustMem(t, tl, "bob", map[string]any{"action": "set", "key": "secret", "value": "bob-data"})
	mustMem(t, tl, "bob", map[string]any{"action": "clear"})
	mustMem(t, tl, "bob", map[string]any{"action": "delete", "key": "log"})

	got := mustMem(t, tl, "alice", map[string]any{"action": "get", "key": "secret"})
	if got["found"] != true || got["value"] != "alice-data" {
		t.Errorf("alice's value after bob's activity = %v", got)
	}
	if got := mustMem(t, tl, "alice", map[string]any{"action": "keys"}); got["count"] != float64(3) {
		t.Errorf("alice's keys = %v", got)
	}
}

func TestMemory_StoresAreExplicitlyOwned(t *testing.T) {
	storeA, storeB := NewMemoryStore(MemoryOptions{}), NewMemoryStore(MemoryOptions{})
	toolA1, toolA2, toolB := NewMemoryTool(storeA), NewMemoryTool(storeA), NewMemoryTool(storeB)

	mustMem(t, toolA1, "s", map[string]any{"action": "set", "key": "k", "value": 1})
	if !found(t, toolA2, "s", "k") {
		t.Error("tools given the same store should share it (deliberate sharing)")
	}
	if found(t, toolB, "s", "k") {
		t.Error("tools with different stores must not share data, even for the same scope")
	}

	// A nil store means a private store, never a hidden global one.
	nil1, nil2 := NewMemoryTool(nil), NewMemoryTool(nil)
	mustMem(t, nil1, "s", map[string]any{"action": "set", "key": "k", "value": 1})
	if found(t, nil2, "s", "k") {
		t.Error("NewMemoryTool(nil) instances must be isolated from each other")
	}
	if found(t, NewMemoryTool(nil), "s", "k") {
		t.Error("data leaked into a fresh tool")
	}
}

func TestMemory_ForgetDropsAScope(t *testing.T) {
	store := NewMemoryStore(MemoryOptions{})
	tl := NewMemoryTool(store)
	mustMem(t, tl, "a", map[string]any{"action": "set", "key": "k", "value": 1})
	mustMem(t, tl, "b", map[string]any{"action": "set", "key": "k", "value": 2})

	store.Forget("a")
	if found(t, tl, "a", "k") || !found(t, tl, "b", "k") {
		t.Error("Forget must remove exactly the given scope")
	}
}

func TestMemory_KeyValueRoundTrip(t *testing.T) {
	tl, _ := newMemory(t, MemoryOptions{})

	values := map[string]any{
		"string": "hello",
		"int":    42,
		"float":  1.5,
		"bool":   true,
		"null":   nil,
		"object": map[string]any{"a": []any{1, "two", map[string]any{"three": 3}}},
		"array":  []any{},
	}
	for key, v := range values {
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": key, "value": v})
		got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": key})
		// Compare with what the JSON encoder makes of the original value.
		b, _ := json.Marshal(v)
		var round any
		_ = json.Unmarshal(b, &round)
		if got["found"] != true || !reflect.DeepEqual(got["value"], round) {
			t.Errorf("%s: stored %v, want %v", key, got, round)
		}
	}

	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "missing"}); got["found"] != false {
		t.Errorf("missing key: %v", got)
	}

	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "string", "value": "replaced"})
	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "string"}); got["value"] != "replaced" {
		t.Errorf("overwrite: %v", got)
	}

	if got := mustMem(t, tl, "s", map[string]any{"action": "delete", "key": "string"}); got["existed"] != true {
		t.Errorf("delete: %v", got)
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "delete", "key": "string"}); got["existed"] != false {
		t.Errorf("second delete: %v", got)
	}
	if found(t, tl, "s", "string") {
		t.Error("deleted key still readable")
	}

	keys := mustMem(t, tl, "s", map[string]any{"action": "keys"})["keys"].([]any)
	for i := 1; i < len(keys); i++ {
		if keys[i-1].(string) > keys[i].(string) {
			t.Errorf("keys are not sorted: %v", keys)
		}
	}
}

func TestMemory_TTLExpiresEverywhere(t *testing.T) {
	tl, clock := newMemory(t, MemoryOptions{})

	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "k", "value": "v", "ttl": 10})
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "forever", "value": "v"})

	clock.Advance(9 * time.Second)
	got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "k"})
	if got["found"] != true || got["ttl_remaining"] != float64(1) || got["expires_at"] == nil {
		t.Errorf("before expiry: %v", got)
	}

	clock.Advance(1 * time.Second)
	if found(t, tl, "s", "k") {
		t.Error("expired value is still readable through get")
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "keys"}); got["count"] != float64(1) {
		t.Errorf("expired key listed by keys: %v", got)
	}
	list := mustMem(t, tl, "s", map[string]any{"action": "list"})
	if list["count"] != float64(1) || list["data"].(map[string]any)["k"] != nil {
		t.Errorf("expired key dumped by list: %v", list)
	}
	if !found(t, tl, "s", "forever") {
		t.Error("a key without ttl must not expire")
	}

	// Writing the key again after expiry brings it back fresh; the old value never resurfaces.
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "k", "value": "new"})
	clock.Advance(time.Hour)
	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "k"}); got["value"] != "new" {
		t.Errorf("rewritten key = %v", got)
	}

	// Overwriting without ttl clears a previous expiry.
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "t", "value": 1, "ttl": 5})
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "t", "value": 2})
	clock.Advance(time.Minute)
	if !found(t, tl, "s", "t") {
		t.Error("set without ttl should replace the expiry")
	}
}

func TestMemory_TTLValidation(t *testing.T) {
	tl, _ := newMemory(t, MemoryOptions{MaxTTL: time.Hour})
	for _, ttl := range []int{-1, 3601, 1 << 40} {
		if _, err := mem(tl, "s", map[string]any{"action": "set", "key": "k", "value": 1, "ttl": ttl}); err == nil {
			t.Errorf("ttl %d accepted", ttl)
		}
	}
	if _, err := mem(tl, "s", map[string]any{"action": "set", "key": "k", "value": 1, "ttl": 3600}); err != nil {
		t.Errorf("ttl at the limit rejected: %v", err)
	}
	if _, err := mem(tl, "s", map[string]any{"action": "set", "key": "k", "value": 1, "ttl": 1.5}); err == nil {
		t.Error("fractional ttl accepted")
	}
}

func TestMemory_Counters(t *testing.T) {
	tl, clock := newMemory(t, MemoryOptions{})
	incr := func(action string, extra map[string]any) (map[string]any, error) {
		in := map[string]any{"action": action, "key": "n"}
		for k, v := range extra {
			in[k] = v
		}
		return mem(tl, "s", in)
	}

	if got, _ := incr("incr", nil); got["previous"] != float64(0) || got["current"] != float64(1) {
		t.Errorf("first incr = %v", got)
	}
	if got, _ := incr("incr", map[string]any{"value": 10}); got["current"] != float64(11) {
		t.Errorf("incr by 10 = %v", got)
	}
	if got, _ := incr("decr", map[string]any{"value": 4}); got["current"] != float64(7) {
		t.Errorf("decr by 4 = %v", got)
	}
	if got, _ := incr("decr", map[string]any{"value": -3}); got["current"] != float64(10) {
		t.Errorf("decr by -3 = %v", got)
	}

	for _, bad := range []any{1.5, "5", true, []int{1}} {
		if _, err := incr("incr", map[string]any{"value": bad}); err == nil {
			t.Errorf("amount %v accepted", bad)
		}
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "n"}); got["value"] != float64(10) {
		t.Errorf("rejected calls changed the counter: %v", got)
	}

	// A live ttl survives incr; the key still expires on schedule.
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "timed", "value": 1, "ttl": 10})
	clock.Advance(4 * time.Second)
	got := mustMem(t, tl, "s", map[string]any{"action": "incr", "key": "timed"})
	if got["current"] != float64(2) {
		t.Errorf("incr on timed key = %v", got)
	}
	if g := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "timed"}); g["ttl_remaining"] != float64(6) {
		t.Errorf("incr must not reset or drop the ttl: %v", g)
	}
	clock.Advance(6 * time.Second)
	if found(t, tl, "s", "timed") {
		t.Error("counter outlived its ttl because incr dropped it")
	}
	// After expiry the counter restarts from zero rather than reviving the old value.
	if got := mustMem(t, tl, "s", map[string]any{"action": "incr", "key": "timed"}); got["previous"] != float64(0) || got["current"] != float64(1) {
		t.Errorf("incr after expiry = %v", got)
	}

	// An explicit ttl on incr is honored.
	mustMem(t, tl, "s", map[string]any{"action": "incr", "key": "fresh", "ttl": 3})
	clock.Advance(3 * time.Second)
	if found(t, tl, "s", "fresh") {
		t.Error("explicit ttl on incr ignored")
	}
}

func TestMemory_CounterNeverSilentlyResets(t *testing.T) {
	tl, _ := newMemory(t, MemoryOptions{})

	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "text", "value": "not a number"})
	if _, err := mem(tl, "s", map[string]any{"action": "incr", "key": "text"}); err == nil {
		t.Error("incr on a string succeeded")
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "text"}); got["value"] != "not a number" {
		t.Errorf("failed incr changed the value: %v", got)
	}

	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "float", "value": 1.5})
	if _, err := mem(tl, "s", map[string]any{"action": "incr", "key": "float"}); err == nil {
		t.Error("incr on a non-integer number succeeded")
	}

	mustMem(t, tl, "s", map[string]any{"action": "append", "key": "list", "value": 1})
	if _, err := mem(tl, "s", map[string]any{"action": "incr", "key": "list"}); err == nil {
		t.Error("incr on a list succeeded")
	}

	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "max", "value": json.Number("9223372036854775807")})
	if _, err := mem(tl, "s", map[string]any{"action": "incr", "key": "max"}); err == nil {
		t.Error("overflow succeeded")
	}
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "min", "value": json.Number("-9223372036854775808")})
	if _, err := mem(tl, "s", map[string]any{"action": "decr", "key": "min"}); err == nil {
		t.Error("underflow succeeded")
	}
}

func TestMemory_CountersRejectNullWithoutChangingValues(t *testing.T) {
	for _, action := range []string{"incr", "decr"} {
		t.Run(action, func(t *testing.T) {
			tl, _ := newMemory(t, MemoryOptions{})
			mustMem(t, tl, "s", map[string]any{"action": "set", "key": "null", "value": nil})
			if _, err := mem(tl, "s", map[string]any{"action": action, "key": "null"}); err == nil {
				t.Error("counter accepted stored null")
			}
			if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "null"}); got["found"] != true || got["value"] != nil {
				t.Errorf("rejected counter changed stored null: %v", got)
			}

			mustMem(t, tl, "s", map[string]any{"action": "set", "key": "n", "value": 9, "ttl": 10})
			before := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "n"})
			if _, err := mem(tl, "s", map[string]any{"action": action, "key": "n", "value": nil, "ttl": 0}); err == nil {
				t.Error("counter accepted explicit null amount")
			}
			after := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "n"})
			if !reflect.DeepEqual(after, before) {
				t.Errorf("null amount changed value or metadata: before %v, after %v", before, after)
			}
			if _, err := mem(tl, "s", map[string]any{"action": action, "key": "missing", "value": nil}); err == nil {
				t.Error("counter accepted explicit null amount on missing key")
			}
			if found(t, tl, "s", "missing") {
				t.Error("null amount created missing counter")
			}
		})
	}
}

func TestMemory_CounterValueQuotaRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   int
		initial int
		action  string
	}{
		{name: "positive digit boundary", limit: 1, initial: 9, action: "incr"},
		{name: "negative sign boundary", limit: 1, initial: 0, action: "decr"},
		{name: "negative digit boundary", limit: 2, initial: -9, action: "decr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Leave exactly two bytes free for another one-byte key and value.
			tl, clock := newMemory(t, MemoryOptions{MaxValueBytes: tc.limit, MaxScopeBytes: 1 + tc.limit + 2})
			mustMem(t, tl, "s", map[string]any{"action": "set", "key": "n", "value": tc.initial, "ttl": 10})
			before := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "n"})
			for range 3 {
				if _, err := mem(tl, "s", map[string]any{"action": tc.action, "key": "n", "ttl": 0}); !errors.Is(err, ErrMemoryQuota) {
					t.Errorf("oversize counter: error = %v, want ErrMemoryQuota", err)
				}
			}
			after := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "n"})
			if !reflect.DeepEqual(after, before) {
				t.Errorf("quota rejection changed value or metadata: before %v, after %v", before, after)
			}
			mustMem(t, tl, "s", map[string]any{"action": "set", "key": "x", "value": 1})
			clock.Advance(10 * time.Second)
			if found(t, tl, "s", "n") {
				t.Error("quota rejection dropped the original ttl")
			}
		})
	}

	t.Run("new negative counter", func(t *testing.T) {
		tl, _ := newMemory(t, MemoryOptions{MaxValueBytes: 1, MaxKeysPerScope: 1, MaxScopeBytes: 3})
		for range 3 {
			if _, err := mem(tl, "s", map[string]any{"action": "decr", "key": "n"}); !errors.Is(err, ErrMemoryQuota) {
				t.Errorf("negative counter: error = %v, want ErrMemoryQuota", err)
			}
		}
		if found(t, tl, "s", "n") {
			t.Error("rejected negative counter created a key")
		}
		if got := mustMem(t, tl, "s", map[string]any{"action": "incr", "key": "n"}); got["current"] != float64(1) {
			t.Errorf("rejected counter leaked quota or state: %v", got)
		}
	})
}

func TestMemory_Lists(t *testing.T) {
	tl, _ := newMemory(t, MemoryOptions{})
	for _, v := range []any{"a", "b", map[string]any{"c": 3}, "d"} {
		mustMem(t, tl, "s", map[string]any{"action": "append", "key": "L", "value": v})
	}

	if got := mustMem(t, tl, "s", map[string]any{"action": "llen", "key": "L"}); got["length"] != float64(4) || got["exists"] != true {
		t.Errorf("llen = %v", got)
	}

	items := func(in map[string]any) string {
		in["action"], in["key"] = "lrange", "L"
		b, _ := json.Marshal(mustMem(t, tl, "s", in)["items"])
		return string(b)
	}
	tests := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"defaults return everything", map[string]any{}, `["a","b",{"c":3},"d"]`},
		{"explicit end 0 is the first item only", map[string]any{"start": 0, "end": 0}, `["a"]`},
		{"end -1 is the last item", map[string]any{"end": -1}, `["a","b",{"c":3},"d"]`},
		{"middle", map[string]any{"start": 1, "end": 2}, `["b",{"c":3}]`},
		{"negative start", map[string]any{"start": -2}, `[{"c":3},"d"]`},
		{"negative both", map[string]any{"start": -3, "end": -2}, `["b",{"c":3}]`},
		{"end beyond length", map[string]any{"start": 2, "end": 99}, `[{"c":3},"d"]`},
		{"start beyond length", map[string]any{"start": 10}, `[]`},
		{"inverted", map[string]any{"start": 3, "end": 1}, `[]`},
		{"start before beginning", map[string]any{"start": -99, "end": 0}, `["a"]`},
	}
	for _, tt := range tests {
		if got := items(tt.in); got != tt.want {
			t.Errorf("%s: lrange %v = %s, want %s", tt.name, tt.in, got, tt.want)
		}
	}

	if got := mustMem(t, tl, "s", map[string]any{"action": "pop", "key": "L"}); got["value"] != "d" || got["length"] != float64(3) {
		t.Errorf("pop = %v", got)
	}

	// Popping the last item removes the key and frees its slot.
	for range 3 {
		mustMem(t, tl, "s", map[string]any{"action": "pop", "key": "L"})
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "pop", "key": "L"}); got["empty"] != true {
		t.Errorf("pop on empty = %v", got)
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "keys"}); got["count"] != float64(0) {
		t.Errorf("an emptied list should not linger: %v", got)
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "lrange", "key": "missing"}); got["length"] != float64(0) {
		t.Errorf("lrange on a missing list = %v", got)
	}
}

func TestMemory_ScalarsAndListsShareOneKeyspace(t *testing.T) {
	tl, clock := newMemory(t, MemoryOptions{})

	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "scalar", "value": 1})
	mustMem(t, tl, "s", map[string]any{"action": "append", "key": "list", "value": 1})

	wrong := []map[string]any{
		{"action": "append", "key": "scalar", "value": 2},
		{"action": "pop", "key": "scalar"},
		{"action": "lrange", "key": "scalar"},
		{"action": "llen", "key": "scalar"},
		{"action": "get", "key": "list"},
		{"action": "incr", "key": "list"},
	}
	for _, in := range wrong {
		if _, err := mem(tl, "s", in); err == nil {
			t.Errorf("%v crossed kinds without error", in)
		}
	}

	if got := mustMem(t, tl, "s", map[string]any{"action": "keys"}); got["count"] != float64(2) {
		t.Errorf("keys = %v", got)
	}
	list := mustMem(t, tl, "s", map[string]any{"action": "list"})["data"].(map[string]any)
	if list["scalar"] != float64(1) || !reflect.DeepEqual(list["list"], []any{float64(1)}) {
		t.Errorf("list = %v", list)
	}

	// set replaces a list wholesale, with the new value's own ttl.
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "list", "value": "now a scalar", "ttl": 5})
	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "list"}); got["value"] != "now a scalar" {
		t.Errorf("set over list = %v", got)
	}

	// ttl applies to lists too, and expired lists vanish from every view.
	mustMem(t, tl, "s", map[string]any{"action": "append", "key": "timed", "value": "x", "ttl": 10})
	mustMem(t, tl, "s", map[string]any{"action": "append", "key": "timed", "value": "y"}) // no ttl: keeps the existing one
	clock.Advance(10 * time.Second)
	if got := mustMem(t, tl, "s", map[string]any{"action": "llen", "key": "timed"}); got["exists"] != false || got["length"] != float64(0) {
		t.Errorf("expired list llen = %v", got)
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "lrange", "key": "timed"}); got["length"] != float64(0) {
		t.Errorf("expired list lrange = %v", got)
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "pop", "key": "timed"}); got["empty"] != true {
		t.Errorf("expired list pop = %v", got)
	}
	mustMem(t, tl, "s", map[string]any{"action": "append", "key": "timed", "value": "fresh"})
	if got := mustMem(t, tl, "s", map[string]any{"action": "lrange", "key": "timed"}); got["total"] != float64(1) {
		t.Errorf("an expired list must not resurface when appended to: %v", got)
	}
}

func TestMemory_ReturnedDataIsNotAliased(t *testing.T) {
	store := NewMemoryStore(MemoryOptions{})
	tl := NewMemoryTool(store)
	ctx := WithMemoryScope(context.Background(), "s")
	run := func(in map[string]any) map[string]any {
		raw, _ := json.Marshal(in)
		out, err := tl.Handler(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}

	run(map[string]any{"action": "append", "key": "L", "value": "first"})
	run(map[string]any{"action": "append", "key": "L", "value": "second"})
	run(map[string]any{"action": "set", "key": "K", "value": "scalar"})

	// Scribble over everything a caller can get its hands on.
	scribble := func(b json.RawMessage) {
		for i := range b {
			b[i] = 'X'
		}
	}
	items := run(map[string]any{"action": "lrange", "key": "L"})["items"].([]json.RawMessage)
	for _, it := range items {
		scribble(it)
	}
	items[0] = nil
	_ = append(items[:1], json.RawMessage(`"injected"`))
	scribble(run(map[string]any{"action": "get", "key": "K"})["value"].(json.RawMessage))
	dump := run(map[string]any{"action": "list"})["data"].(map[string]any)
	for _, it := range dump["L"].([]json.RawMessage) {
		scribble(it)
	}
	scribble(dump["K"].(json.RawMessage))
	popped := run(map[string]any{"action": "pop", "key": "L"})["value"].(json.RawMessage)
	scribble(popped)

	if got := mustMem(t, tl, "s", map[string]any{"action": "lrange", "key": "L"}); !reflect.DeepEqual(got["items"], []any{"first"}) {
		t.Errorf("list was corrupted through a returned slice: %v", got["items"])
	}
	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "K"}); got["value"] != "scalar" {
		t.Errorf("scalar was corrupted through a returned value: %v", got)
	}
}

func TestMemory_ConcurrentUseIsRaceFree(t *testing.T) {
	tl, _ := newMemory(t, MemoryOptions{})
	ctx := WithMemoryScope(context.Background(), "shared")

	call := func(in map[string]any) {
		raw, _ := json.Marshal(in)
		out, err := tl.Handler(ctx, raw)
		if err != nil {
			return // quota or wrong-kind errors are fine under contention
		}
		// Encoding the result happens after the store released its lock.
		if _, err := json.Marshal(out); err != nil {
			t.Errorf("result not encodable: %v", err)
		}
	}

	var wg sync.WaitGroup
	loop := func(f func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				f(i)
			}
		}()
	}
	for range 2 {
		loop(func(i int) { call(map[string]any{"action": "append", "key": "L", "value": i}) })
		loop(func(i int) { call(map[string]any{"action": "pop", "key": "L"}) })
		loop(func(i int) { call(map[string]any{"action": "lrange", "key": "L", "start": 0, "end": -1}) })
		loop(func(i int) { call(map[string]any{"action": "list"}) })
		loop(func(i int) { call(map[string]any{"action": "keys"}) })
		loop(func(i int) { call(map[string]any{"action": "llen", "key": "L"}) })
		loop(func(i int) { call(map[string]any{"action": "incr", "key": "n", "ttl": 60}) })
		loop(func(i int) { call(map[string]any{"action": "get", "key": "n"}) })
		loop(func(i int) { call(map[string]any{"action": "set", "key": "S", "value": map[string]int{"i": i}}) })
	}
	wg.Wait()
}

func TestMemory_ReadersCannotDeleteNewlyWrittenValues(t *testing.T) {
	tl, clock := newMemory(t, MemoryOptions{})
	mustMem(t, tl, "s", map[string]any{"action": "set", "key": "k", "value": "old", "ttl": 1})
	clock.Advance(2 * time.Second) // "old" is now expired but not yet swept

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_, _ = mem(tl, "s", map[string]any{"action": "get", "key": "k"})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = mem(tl, "s", map[string]any{"action": "set", "key": "k", "value": "new"})
	}()
	wg.Wait()

	if got := mustMem(t, tl, "s", map[string]any{"action": "get", "key": "k"}); got["found"] != true || got["value"] != "new" {
		t.Errorf("a stale reader deleted the fresh value: %v", got)
	}
}

func TestMemory_QuotasAreEnforcedAndReleased(t *testing.T) {
	quota := func(t *testing.T, tl adapter.Tool, scope string, in map[string]any) {
		t.Helper()
		if _, err := mem(tl, scope, in); !errors.Is(err, ErrMemoryQuota) {
			t.Errorf("%v: error = %v, want ErrMemoryQuota", in, err)
		}
	}

	t.Run("keys per scope", func(t *testing.T) {
		tl, _ := newMemory(t, MemoryOptions{MaxKeysPerScope: 2})
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "a", "value": 1})
		mustMem(t, tl, "s", map[string]any{"action": "append", "key": "b", "value": 1})
		quota(t, tl, "s", map[string]any{"action": "set", "key": "c", "value": 1})
		quota(t, tl, "s", map[string]any{"action": "append", "key": "d", "value": 1})
		quota(t, tl, "s", map[string]any{"action": "incr", "key": "e"})
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "a", "value": 2}) // overwrite needs no new slot
		mustMem(t, tl, "other", map[string]any{"action": "set", "key": "a", "value": 1})

		mustMem(t, tl, "s", map[string]any{"action": "delete", "key": "a"})
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "c", "value": 1}) // freed by delete
	})

	t.Run("value size", func(t *testing.T) {
		tl, _ := newMemory(t, MemoryOptions{MaxValueBytes: 10})
		quota(t, tl, "s", map[string]any{"action": "set", "key": "k", "value": "this value is too long"})
		quota(t, tl, "s", map[string]any{"action": "append", "key": "k", "value": "this value is too long"})
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "k", "value": "short"})
	})

	t.Run("list length", func(t *testing.T) {
		tl, _ := newMemory(t, MemoryOptions{MaxListLen: 2})
		mustMem(t, tl, "s", map[string]any{"action": "append", "key": "L", "value": 1})
		mustMem(t, tl, "s", map[string]any{"action": "append", "key": "L", "value": 2})
		quota(t, tl, "s", map[string]any{"action": "append", "key": "L", "value": 3})
		if got := mustMem(t, tl, "s", map[string]any{"action": "llen", "key": "L"}); got["length"] != float64(2) {
			t.Errorf("rejected append changed the list: %v", got)
		}
		mustMem(t, tl, "s", map[string]any{"action": "pop", "key": "L"})
		mustMem(t, tl, "s", map[string]any{"action": "append", "key": "L", "value": 3})
	})

	t.Run("bytes per scope", func(t *testing.T) {
		tl, _ := newMemory(t, MemoryOptions{MaxScopeBytes: 40})
		big := strings.Repeat("x", 25) // 27 encoded + 2-byte key
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "k1", "value": big})
		quota(t, tl, "s", map[string]any{"action": "set", "key": "k2", "value": big})
		quota(t, tl, "s", map[string]any{"action": "append", "key": "k2", "value": big})

		// Repeated rejections must not leak accounting.
		for range 20 {
			quota(t, tl, "s", map[string]any{"action": "set", "key": "k2", "value": big})
		}
		mustMem(t, tl, "s", map[string]any{"action": "delete", "key": "k1"})
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "k2", "value": big})

		// Replacing a value only charges the difference.
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "k2", "value": big})
		mustMem(t, tl, "s", map[string]any{"action": "clear"})
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "k3", "value": big})
	})

	t.Run("expiry frees capacity", func(t *testing.T) {
		tl, clock := newMemory(t, MemoryOptions{MaxKeysPerScope: 1, MaxScopeBytes: 40})
		big := strings.Repeat("x", 25)
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "a", "value": big, "ttl": 5})
		quota(t, tl, "s", map[string]any{"action": "set", "key": "b", "value": big})
		clock.Advance(5 * time.Second)
		mustMem(t, tl, "s", map[string]any{"action": "set", "key": "b", "value": big})
	})

	t.Run("scopes", func(t *testing.T) {
		tl, clock := newMemory(t, MemoryOptions{MaxScopes: 1})
		mustMem(t, tl, "a", map[string]any{"action": "set", "key": "k", "value": 1, "ttl": 5})
		quota(t, tl, "b", map[string]any{"action": "set", "key": "k", "value": 1})
		// Reads never allocate a scope, even when the table is full.
		if found(t, tl, "c", "k") {
			t.Error("unexpected data")
		}
		mustMem(t, tl, "c", map[string]any{"action": "keys"})
		clock.Advance(5 * time.Second)
		mustMem(t, tl, "b", map[string]any{"action": "set", "key": "k", "value": 1}) // a's expired data no longer holds the slot
		quota(t, tl, "a", map[string]any{"action": "set", "key": "k", "value": 1})

		// Clearing or emptying a scope also frees its slot.
		mustMem(t, tl, "b", map[string]any{"action": "clear"})
		mustMem(t, tl, "a", map[string]any{"action": "set", "key": "k", "value": 1})
	})
}

func TestMemory_RejectsInvalidCalls(t *testing.T) {
	tl, _ := newMemory(t, MemoryOptions{})
	bad := []map[string]any{
		{"action": ""},
		{"action": "explode"},
		{"action": "SET", "key": "k", "value": 1},
		{"action": "set", "value": 1},                                  // no key
		{"action": "set", "key": "k"},                                  // no value
		{"action": "append", "key": "k"},                               // no value
		{"action": "get"},                                              // no key
		{"action": "set", "key": strings.Repeat("k", 257), "value": 1}, // key too long
		{"action": "lrange", "key": "k", "start": "zero"},
		{"action": "set", "key": "k", "value": 1, "ttl": "soon"},
	}
	for _, in := range bad {
		if _, err := mem(tl, "s", in); err == nil {
			t.Errorf("%v accepted", in)
		}
	}
	if _, err := tl.Handler(WithMemoryScope(context.Background(), "s"), json.RawMessage(`{"action":"keys"} {"action":"clear"}`)); err == nil {
		t.Error("two JSON values accepted")
	}
	if _, err := tl.Handler(WithMemoryScope(context.Background(), strings.Repeat("s", 300)), json.RawMessage(`{"action":"keys"}`)); err == nil {
		t.Error("oversized scope accepted")
	}
}
