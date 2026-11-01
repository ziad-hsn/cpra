package persistence

import "github.com/ziad-hsn/cpra/sdk/go/api"

const maxRecoveryProgramCacheBytes = 64 << 20
const maxRecoveryProgramCacheEntries = 16384

type recoveryProgramEntry struct {
	program *api.CompiledRules
	users   uint64
	bytes   uint64
}

// The cache changes only sharing, never selection or admission. It is bounded
// even with unique policies. Programs that do not fit remain privately owned by
// their policy; they do not evict programs still used by another monitor. Frozen
// snapshots retain their own immutable references after the last live user exits.
type recoveryProgramCache struct {
	entries map[string]*recoveryProgramEntry
	bytes   uint64
}

func (c *recoveryProgramCache) replace(previous RecoveryRules, next RecoveryRules) RecoveryRules {
	oldKey, newKey := "", ""
	if previous.data != nil {
		oldKey = previous.data.compiled.Digest()
	}
	if next.data != nil {
		newKey = next.data.compiled.Digest()
	}
	old, owned := c.entries[oldKey]
	owned = owned && previous.data != nil && previous.data.intern == old
	if oldKey != newKey && owned {
		old.users--
		if old.users == 0 {
			delete(c.entries, oldKey)
			c.bytes -= old.bytes
		}
	}
	if newKey == "" {
		return next
	}
	entry, found := c.entries[newKey]
	if !found {
		size := next.data.compiled.MemoryBytes()
		if size > maxRecoveryProgramCacheBytes-c.bytes || len(c.entries) >= maxRecoveryProgramCacheEntries {
			return next
		}
		if c.entries == nil {
			c.entries = make(map[string]*recoveryProgramEntry)
		}
		entry = &recoveryProgramEntry{program: next.data.compiled, bytes: size}
		c.bytes += size
	}
	if !owned || old != entry {
		entry.users++
	}
	c.entries[newKey] = entry
	if entry.program == next.data.compiled && next.data.intern == entry {
		return next
	}
	data := *next.data
	data.compiled = entry.program
	data.intern = entry
	return RecoveryRules{data: &data}
}
