package tool

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dvictor357/blaze/adapter"
)

// MemoryStore is an in-memory key-value store with TTL support.
// It persists data for the lifetime of the process.
type MemoryStore struct {
	mu    sync.RWMutex
	data  map[string]memoryEntry
	lists map[string][]any
}

type memoryEntry struct {
	Value     any       `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	TTL       int       `json:"ttl_seconds,omitempty"`
}

// Global memory store instance
var globalMemory = &MemoryStore{
	data:  make(map[string]memoryEntry),
	lists: make(map[string][]any),
}

// NewMemoryTool creates a tool for storing and retrieving data in memory.
// This allows the AI to persist information across tool calls within a session.
// Supports:
// - Key-value storage with optional TTL
// - Lists (append, pop, range)
// - Counters (increment, decrement)
func NewMemoryTool() adapter.Tool {
	return adapter.NewTool(
		"memory",
		"Store and retrieve data in memory. Use this to remember information across tool calls, create lists, or track counters. Data persists for the server lifetime.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"set", "get", "delete", "list", "keys", "clear", "incr", "decr", "append", "pop", "lrange", "llen"},
					"description": "Action: 'set/get/delete' for key-value, 'incr/decr' for counters, 'append/pop/lrange/llen' for lists, 'keys' to list all keys, 'list' to dump all, 'clear' to reset",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Key name for the data",
				},
				"value": map[string]any{
					"description": "Value to store (any JSON type)",
				},
				"ttl": map[string]any{
					"type":        "integer",
					"description": "Time-to-live in seconds (0 = no expiry)",
				},
				"start": map[string]any{
					"type":        "integer",
					"description": "Start index for lrange (default: 0)",
				},
				"end": map[string]any{
					"type":        "integer",
					"description": "End index for lrange (default: -1 for all)",
				},
			},
			"required": []string{"action"},
		},
		func(input json.RawMessage) (any, error) {
			var data struct {
				Action string `json:"action"`
				Key    string `json:"key"`
				Value  any    `json:"value"`
				TTL    int    `json:"ttl"`
				Start  int    `json:"start"`
				End    int    `json:"end"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				return nil, fmt.Errorf("invalid input: %w", err)
			}

			switch data.Action {
			case "set":
				if data.Key == "" {
					return nil, fmt.Errorf("key is required for set")
				}
				return globalMemory.Set(data.Key, data.Value, data.TTL)

			case "get":
				if data.Key == "" {
					return nil, fmt.Errorf("key is required for get")
				}
				return globalMemory.Get(data.Key)

			case "delete":
				if data.Key == "" {
					return nil, fmt.Errorf("key is required for delete")
				}
				return globalMemory.Delete(data.Key)

			case "keys":
				return globalMemory.Keys()

			case "list":
				return globalMemory.List()

			case "clear":
				return globalMemory.Clear()

			case "incr":
				if data.Key == "" {
					return nil, fmt.Errorf("key is required for incr")
				}
				amount := 1
				if data.Value != nil {
					if v, ok := data.Value.(float64); ok {
						amount = int(v)
					}
				}
				return globalMemory.Incr(data.Key, amount)

			case "decr":
				if data.Key == "" {
					return nil, fmt.Errorf("key is required for decr")
				}
				amount := 1
				if data.Value != nil {
					if v, ok := data.Value.(float64); ok {
						amount = int(v)
					}
				}
				return globalMemory.Incr(data.Key, -amount)

			case "append":
				if data.Key == "" {
					return nil, fmt.Errorf("key is required for append")
				}
				return globalMemory.ListAppend(data.Key, data.Value)

			case "pop":
				if data.Key == "" {
					return nil, fmt.Errorf("key is required for pop")
				}
				return globalMemory.ListPop(data.Key)

			case "lrange":
				if data.Key == "" {
					return nil, fmt.Errorf("key is required for lrange")
				}
				end := -1
				if data.End != 0 {
					end = data.End
				}
				return globalMemory.ListRange(data.Key, data.Start, end)

			case "llen":
				if data.Key == "" {
					return nil, fmt.Errorf("key is required for llen")
				}
				return globalMemory.ListLen(data.Key)

			default:
				return nil, fmt.Errorf("unknown action: %s", data.Action)
			}
		},
	)
}

// Set stores a value with optional TTL
func (m *MemoryStore) Set(key string, value any, ttlSeconds int) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := memoryEntry{
		Value:     value,
		CreatedAt: time.Now(),
	}

	if ttlSeconds > 0 {
		entry.ExpiresAt = time.Now().Add(time.Duration(ttlSeconds) * time.Second)
		entry.TTL = ttlSeconds
	}

	m.data[key] = entry

	return map[string]any{
		"success": true,
		"key":     key,
		"ttl":     ttlSeconds,
	}, nil
}

// Get retrieves a value by key
func (m *MemoryStore) Get(key string) (map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.data[key]
	if !exists {
		return map[string]any{
			"found": false,
			"key":   key,
		}, nil
	}

	// Check TTL
	if !entry.ExpiresAt.IsZero() && time.Now().After(entry.ExpiresAt) {
		// Key has expired - delete it
		m.mu.RUnlock()
		m.mu.Lock()
		delete(m.data, key)
		m.mu.Unlock()
		m.mu.RLock()

		return map[string]any{
			"found":   false,
			"key":     key,
			"expired": true,
		}, nil
	}

	result := map[string]any{
		"found":      true,
		"key":        key,
		"value":      entry.Value,
		"created_at": entry.CreatedAt.Format(time.RFC3339),
	}

	if !entry.ExpiresAt.IsZero() {
		result["expires_at"] = entry.ExpiresAt.Format(time.RFC3339)
		result["ttl_remaining"] = int(time.Until(entry.ExpiresAt).Seconds())
	}

	return result, nil
}

// Delete removes a key
func (m *MemoryStore) Delete(key string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, existed := m.data[key]
	delete(m.data, key)
	delete(m.lists, key)

	return map[string]any{
		"success": true,
		"key":     key,
		"existed": existed,
	}, nil
}

// Keys returns all keys
func (m *MemoryStore) Keys() (map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	keys := make([]string, 0, len(m.data)+len(m.lists))

	for k := range m.data {
		keys = append(keys, k)
	}
	for k := range m.lists {
		if _, exists := m.data[k]; !exists {
			keys = append(keys, k+"(list)")
		}
	}

	return map[string]any{
		"keys":  keys,
		"count": len(keys),
	}, nil
}

// List returns all data
func (m *MemoryStore) List() (map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]any)
	for k, v := range m.data {
		result[k] = v.Value
	}
	for k, v := range m.lists {
		result[k+"(list)"] = v
	}

	return map[string]any{
		"data":  result,
		"count": len(result),
	}, nil
}

// Clear removes all data
func (m *MemoryStore) Clear() (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := len(m.data) + len(m.lists)
	m.data = make(map[string]memoryEntry)
	m.lists = make(map[string][]any)

	return map[string]any{
		"success": true,
		"cleared": count,
	}, nil
}

// Incr increments a counter
func (m *MemoryStore) Incr(key string, amount int) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := 0
	if entry, exists := m.data[key]; exists {
		if v, ok := entry.Value.(float64); ok {
			current = int(v)
		} else if v, ok := entry.Value.(int); ok {
			current = v
		}
	}

	newValue := current + amount
	m.data[key] = memoryEntry{
		Value:     float64(newValue),
		CreatedAt: time.Now(),
	}

	return map[string]any{
		"key":      key,
		"previous": current,
		"current":  newValue,
	}, nil
}

// ListAppend adds an item to a list
func (m *MemoryStore) ListAppend(key string, value any) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.lists[key]; !exists {
		m.lists[key] = []any{}
	}

	m.lists[key] = append(m.lists[key], value)

	return map[string]any{
		"key":    key,
		"length": len(m.lists[key]),
	}, nil
}

// ListPop removes and returns the last item
func (m *MemoryStore) ListPop(key string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	list, exists := m.lists[key]
	if !exists || len(list) == 0 {
		return map[string]any{
			"key":   key,
			"empty": true,
		}, nil
	}

	item := list[len(list)-1]
	m.lists[key] = list[:len(list)-1]

	return map[string]any{
		"key":    key,
		"value":  item,
		"length": len(m.lists[key]),
	}, nil
}

// ListRange returns a slice of the list
func (m *MemoryStore) ListRange(key string, start, end int) (map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list, exists := m.lists[key]
	if !exists {
		return map[string]any{
			"key":    key,
			"items":  []any{},
			"length": 0,
		}, nil
	}

	// Handle negative indices
	if start < 0 {
		start = len(list) + start
	}
	if end < 0 {
		end = len(list) + end + 1
	} else {
		end = end + 1
	}

	// Bounds checking
	if start < 0 {
		start = 0
	}
	if end > len(list) {
		end = len(list)
	}
	if start >= end {
		return map[string]any{
			"key":    key,
			"items":  []any{},
			"length": 0,
		}, nil
	}

	return map[string]any{
		"key":    key,
		"items":  list[start:end],
		"length": end - start,
		"total":  len(list),
	}, nil
}

// ListLen returns the length of a list
func (m *MemoryStore) ListLen(key string) (map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list, exists := m.lists[key]
	length := 0
	if exists {
		length = len(list)
	}

	return map[string]any{
		"key":    key,
		"length": length,
		"exists": exists,
	}, nil
}
