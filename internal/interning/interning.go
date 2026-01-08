// Package interning provides string interning functionality to reduce memory
// allocations for frequently repeated string values.
//
// String interning deduplicates string instances by maintaining a global pool
// of unique string values. When the same string is interned multiple times, the
// same underlying string instance is returned, reducing memory usage and
// improving cache locality.
//
// This package is particularly useful for CPRA's large-scale deployments where
// monitor names, HTTP methods, and other low-cardinality strings are repeated
// across millions of entities.
//
// # Thread Safety
//
// The Intern function is safe for concurrent use by multiple goroutines.
// It uses sync.Map which is optimized for the interning use case: entries
// are written once and read many times (2-3x faster than RWMutex at high concurrency).
//
// # Memory Considerations
//
// Interned strings are never garbage collected, so this package should only
// be used for strings with low cardinality (e.g., HTTP methods, common monitor
// name prefixes). For high-cardinality data, interning can lead to unbounded
// memory growth.
//
// # Example
//
//	method1 := interning.Intern("GET")
//	method2 := interning.Intern("GET")
//	// method1 and method2 are the same underlying string instance
//	// This reduces memory when creating millions of HTTP jobs
package interning

import (
	"strings"
	"sync"
	"sync/atomic"
)

	// internedStrings maintains a global pool of interned string instances.
// Uses sync.Map for optimal read performance at high concurrency.
	// Strings are never removed from this map, so it should only be used
	// for low-cardinality data.
var internedStrings sync.Map // map[string]string

var (
	maxInternedStrings = 10000 // Limit to prevent unbounded growth
	internedCount      int64
)

// Intern returns a deduplicated string instance, reducing duplicated allocations
// for low-cardinality data such as monitor names or HTTP methods.
//
// Intern maintains a global pool of unique string values. When called with a
// string that has been interned before, it returns the same underlying string
// instance. For new strings, it creates a new entry in the pool.
//
// The function is safe for concurrent use. It uses sync.Map which provides:
// - Lock-free reads for cached entries (the common case)
// - Automatic sharding to reduce contention at high concurrency
// - 2-3x faster than RWMutex for read-heavy workloads
//
// Parameters:
//   - s: The string to intern. Empty strings are returned as-is without interning.
//
// Returns:
//   - string: An interned string instance. If s is empty, returns empty string.
//     For non-empty strings, returns the same instance for identical inputs.
//
// Memory Warning:
//
//	Interned strings are never garbage collected. Only use this function for
//	strings with low cardinality (e.g., HTTP methods, common prefixes).
//
// Example:
//
//	// Intern HTTP method to reduce allocations across millions of jobs
//	method := interning.Intern("GET")
//	job.Method = method  // Reuse same string instance
func Intern(s string) string {
	if s == "" {
		return ""
	}

	// Fast path: check if already interned (lock-free read)
	if v, ok := internedStrings.Load(s); ok {
		return v.(string)
	}

	// Enforce upper bound to prevent unbounded growth
	if atomic.LoadInt64(&internedCount) >= int64(maxInternedStrings) {
		return strings.Clone(s)
	}

	// Slow path: intern the string
	clone := strings.Clone(s)
	actual, loaded := internedStrings.LoadOrStore(clone, clone)
	if !loaded {
		atomic.AddInt64(&internedCount, 1)
	}
	return actual.(string)
}

// GetInternedCount returns the current number of interned strings.
func GetInternedCount() int64 {
	return atomic.LoadInt64(&internedCount)
}

// ClearInterned clears all interned strings (for testing only).
func ClearInterned() {
	internedStrings = sync.Map{}
	atomic.StoreInt64(&internedCount, 0)
}
