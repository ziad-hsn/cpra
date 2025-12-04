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
// It uses read-write locks to optimize for the common case (cache hits).
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
)

var (
	// internedStrings maintains a global pool of interned string instances.
	// Strings are never removed from this map, so it should only be used
	// for low-cardinality data.
	internedStrings = make(map[string]string)
	// internMu protects access to internedStrings for thread-safe operations.
	internMu sync.RWMutex
)

// Intern returns a deduplicated string instance, reducing duplicated allocations
// for low-cardinality data such as monitor names or HTTP methods.
//
// Intern maintains a global pool of unique string values. When called with a
// string that has been interned before, it returns the same underlying string
// instance. For new strings, it creates a new entry in the pool.
//
// The function is safe for concurrent use and uses read-write locks to optimize
// for cache hits (when the string is already interned).
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
	internMu.RLock()
	if v, ok := internedStrings[s]; ok {
		internMu.RUnlock()
		return v
	}
	internMu.RUnlock()

	clone := strings.Clone(s)
	internMu.Lock()
	if v, ok := internedStrings[clone]; ok {
		internMu.Unlock()
		return v
	}
	internedStrings[clone] = clone
	internMu.Unlock()
	return clone
}
