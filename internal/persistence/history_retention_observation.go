package persistence

import "time"

// publishRetentionCutoff publishes a process-local copy of a committed retention
// decision. Startup initializes it from the recovered catalog; failed saves do
// not publish. Max semantics prevent a later caller clock from moving it back.
func (h *HistoryStore) publishRetentionCutoff(cutoff time.Time) {
	if cutoff.IsZero() {
		return
	}
	cutoff = cutoff.UTC()
	for {
		prior := h.retainedCutoff.Load()
		if prior != nil && !cutoff.After(*prior) {
			return
		}
		if h.retainedCutoff.CompareAndSwap(prior, &cutoff) {
			return
		}
	}
}

// retentionCutoffReached is a lock-free metadata check for the final response
// admission after a protected history read. It does not establish availability.
func (h *HistoryStore) retentionCutoffReached(finalizedAt time.Time) bool {
	cutoff := h.retainedCutoff.Load()
	return cutoff != nil && !finalizedAt.After(*cutoff)
}
