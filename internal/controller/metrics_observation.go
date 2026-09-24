package controller

import "unicode/utf8"

// MetricsSnapshot copies at most limit registered systems. Overflow is explicit;
// callers must not present a truncated snapshot as complete. Its cost depends on
// registered systems, never monitor count. Sorting belongs outside this lock.
func (ma *MetricsAggregator) MetricsSnapshot(limit int) ([]SystemMetrics, bool) {
	if ma == nil || limit < 1 || limit > 256 {
		return nil, false
	}
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if len(ma.systems) > limit {
		return nil, false
	}
	result := make([]SystemMetrics, 0, len(ma.systems))
	for _, metrics := range ma.systems {
		if metrics == nil || len(metrics.SystemName) > 256 || !utf8.ValidString(metrics.SystemName) {
			return nil, false
		}
		result = append(result, *metrics)
	}
	return result, true
}
