package components

import (
	"strconv"
	"sync"

	"cpra/internal/platform/loader/schema"

	"github.com/cespare/xxhash/v2"
)

// ConfigID is a compact handle to a deduplicated ColorCodeConfig.
// Zero is reserved to mean "no config".
type ConfigID uint32

type canonicalKey struct {
	Type        string
	Value       string
	Notify      string
	MaxFailures int
	Dispatch    bool
}

type registryEntry struct {
	id  ConfigID
	key canonicalKey
	cfg ColorCodeConfig
}

// ConfigRegistry implements a flyweight cache for ColorCodeConfig values.
// It is safe for concurrent readers; writes are serialized.
type ConfigRegistry struct {
	mu      sync.RWMutex
	pool    []ColorCodeConfig
	buckets map[uint64][]registryEntry
}

var (
	defaultRegistry    *ConfigRegistry
	defaultRegistryMu  sync.RWMutex
	preInitCapacity    int // Set before first access to DefaultConfigRegistry
)

// InitDefaultConfigRegistry pre-sizes the default registry with a custom capacity.
// Must be called before any calls to DefaultConfigRegistry(). If called after
// initialization, this is a no-op. Typical usage: call with estimated unique
// config count (e.g., monitorCount / 20 for 5% estimate) before loading monitors.
func InitDefaultConfigRegistry(capacity int) {
	defaultRegistryMu.Lock()
	defer defaultRegistryMu.Unlock()
	if capacity > 0 && defaultRegistry == nil {
		preInitCapacity = capacity
	}
}

// initDefaultRegistry initializes the default registry with configured capacity.
// Thread-safe and idempotent.
func initDefaultRegistry() *ConfigRegistry {
	defaultRegistryMu.Lock()
	defer defaultRegistryMu.Unlock()
	if defaultRegistry == nil {
		cap := preInitCapacity
		if cap <= 0 {
			cap = 64 // Default capacity
		}
		defaultRegistry = NewConfigRegistry(cap)
	}
	return defaultRegistry
}

// DefaultConfigRegistry returns the process-wide registry for ColorCodeConfig flyweights.
// For new code, prefer injecting a ConfigRegistry instance directly.
func DefaultConfigRegistry() *ConfigRegistry {
	defaultRegistryMu.RLock()
	reg := defaultRegistry
	defaultRegistryMu.RUnlock()
	if reg != nil {
		return reg
	}
	return initDefaultRegistry()
}

// SetDefaultConfigRegistry replaces the default config registry.
// This is primarily for testing and dependency injection.
// Call this at startup before any configuration operations begin.
//
// Example usage in tests:
//
//	func TestMySystem(t *testing.T) {
//	    reg := NewConfigRegistry(100)
//	    SetDefaultConfigRegistry(reg)
//	    defer SetDefaultConfigRegistry(nil)
//	    // ... run tests
//	}
func SetDefaultConfigRegistry(reg *ConfigRegistry) {
	defaultRegistryMu.Lock()
	defer defaultRegistryMu.Unlock()
	defaultRegistry = reg
}

// ResetDefaultConfigRegistry resets the default config registry to nil.
// This forces re-initialization on next DefaultConfigRegistry() call.
// Primarily for testing.
func ResetDefaultConfigRegistry() {
	defaultRegistryMu.Lock()
	defer defaultRegistryMu.Unlock()
	defaultRegistry = nil
	preInitCapacity = 0
}

// NewConfigRegistry creates a registry with an optional initial capacity.
func NewConfigRegistry(capacity int) *ConfigRegistry {
	if capacity <= 0 {
		capacity = 64
	}
	return &ConfigRegistry{
		pool:    make([]ColorCodeConfig, 0, capacity),
		buckets: make(map[uint64][]registryEntry, capacity),
	}
}

// GetOrAdd returns the canonical ConfigID for the given config, inserting if missing.
// Returns 0 if the config is invalid or incomplete.
func (r *ConfigRegistry) GetOrAdd(cfg ColorCodeConfig) ConfigID {
	key, ok := makeCanonicalKey(cfg)
	if !ok {
		return 0
	}
	hash := hashKey(key)

	r.mu.RLock()
	if id, found := r.lookupLocked(hash, key); found {
		r.mu.RUnlock()
		return id
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if id, found := r.lookupLocked(hash, key); found {
		return id
	}

	id := ConfigID(len(r.pool) + 1) // reserve 0 as "no config"
	entry := registryEntry{
		id:  id,
		key: key,
		cfg: cfg, // value copy; configs are treated as immutable
	}
	r.pool = append(r.pool, cfg)
	r.buckets[hash] = append(r.buckets[hash], entry)
	return id
}

// Lookup returns the ColorCodeConfig for the given ID.
// If the ID is zero or out of range, ok is false.
func (r *ConfigRegistry) Lookup(id ConfigID) (ColorCodeConfig, bool) {
	if id == 0 {
		return ColorCodeConfig{}, false
	}
	idx := int(id - 1)

	r.mu.RLock()
	defer r.mu.RUnlock()
	if idx < 0 || idx >= len(r.pool) {
		return ColorCodeConfig{}, false
	}
	return r.pool[idx], true
}

// lookupLocked assumes r.mu is already held.
func (r *ConfigRegistry) lookupLocked(hash uint64, key canonicalKey) (ConfigID, bool) {
	entries := r.buckets[hash]
	for _, e := range entries {
		if equalKey(e.key, key) {
			return e.id, true
		}
	}
	return 0, false
}

// SetConfig converts and stores a config as an ID for the given color.
// Passing a nil registry is a no-op.
func (c *CodeConfig) SetConfig(color ColorCode, cfg ColorCodeConfig, reg *ConfigRegistry) {
	if reg == nil {
		return
	}
	if color >= MaxColors {
		return
	}
	id := reg.GetOrAdd(cfg)
	c.Configs[color] = id
}

// Resolve returns the concrete ColorCodeConfig for the given color using the registry.
func (c *CodeConfig) Resolve(color ColorCode, reg *ConfigRegistry) (ColorCodeConfig, bool) {
	if reg == nil {
		return ColorCodeConfig{}, false
	}
	if color >= MaxColors {
		return ColorCodeConfig{}, false
	}
	return reg.Lookup(c.Configs[color])
}

func makeCanonicalKey(cfg ColorCodeConfig) (canonicalKey, bool) {
	if cfg.Config == nil || cfg.Notify == "" {
		return canonicalKey{}, false
	}
	key := canonicalKey{
		Notify:      cfg.Notify,
		MaxFailures: cfg.MaxFailures,
		Dispatch:    cfg.Dispatch,
	}

	switch c := cfg.Config.(type) {
	case *schema.CodeNotificationLog:
		key.Type = "log"
		key.Value = c.File
	case *schema.CodeNotificationPagerDuty:
		key.Type = "pagerduty"
		key.Value = c.URL
	case *schema.CodeNotificationSlack:
		key.Type = "slack"
		key.Value = c.WebHook
	default:
		return canonicalKey{}, false
	}
	return key, true
}

func hashKey(k canonicalKey) uint64 {
	buf := make([]byte, 0, len(k.Type)+len(k.Value)+len(k.Notify)+18)
	buf = append(buf, k.Type...)
	buf = append(buf, 0)
	buf = append(buf, k.Value...)
	buf = append(buf, 0)
	buf = append(buf, k.Notify...)
	buf = append(buf, 0)
	buf = strconv.AppendInt(buf, int64(k.MaxFailures), 10)
	buf = append(buf, 0)
	if k.Dispatch {
		buf = append(buf, '1')
	} else {
		buf = append(buf, '0')
	}
	return xxhash.Sum64(buf)
}

func equalKey(a, b canonicalKey) bool {
	return a.Type == b.Type &&
		a.Value == b.Value &&
		a.Notify == b.Notify &&
		a.MaxFailures == b.MaxFailures &&
		a.Dispatch == b.Dispatch
}
