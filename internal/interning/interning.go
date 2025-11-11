package interning

import (
	"strings"
	"sync"
)

var (
	internedStrings = make(map[string]string)
	internMu        sync.RWMutex
)

// Intern returns a deduplicated string instance, reducing duplicated allocations
// for low-cardinality data such as monitor names or HTTP methods.
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
