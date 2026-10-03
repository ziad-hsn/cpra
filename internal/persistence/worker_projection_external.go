//go:build externaljobs

package persistence

import "context"

// A finalized check generation belongs to monitor state, not receipt retention.
// Use authenticated catalog reference metadata without opening protected values
// or walking completed executions. The ordinary monitor-index pass owns this
// lookup, including after a snapshot restore or control-cursor gap.
func (f *machine) hasWorkerCheckState(m Monitor) bool {
	if m.Generation == 0 || m.Removed || m.CatalogUID == "" {
		return false
	}
	source, ok := f.image.Catalog[(CatalogKey{Kind: "Monitor", ID: m.ID}).indexKey()]
	if !ok || source.Removed || source.UID != m.CatalogUID {
		return false
	}
	for _, reference := range source.JobTypeReferences {
		if reference.Category == "check" {
			return true
		}
	}
	return false
}

// publishWorkerMonitor uses the owner projection stream for committed worker
// changes even when incident and control versions are unchanged. Only the owner
// consumes this hint; the current monitor remains the authoritative projection.
func (f *machine) publishWorkerMonitor(previous, current Monitor) {
	f.controls.ReplaceOrInsert(changeOf(current))
	if changeOf(previous) == changeOf(current) && incidentOf(previous).Active == incidentOf(current).Active {
		f.publishControlChange(current)
	}
}

// indexExtensionMonitors includes healthy external monitors in a fresh owner
// view. A cursor gap or restart must not lose a result merely because it changed
// neither controls nor incident membership. The execution namespace is bounded;
// no encrypted payload, result or action map is copied here.
func (f *machine) indexExtensionMonitors(ctx context.Context) error {
	state := f.image.WorkerExecutions
	if state == nil {
		return ctx.Err()
	}
	if len(state.Records) > MaxWorkerExecutions {
		return ErrWorkerExecutionQuota
	}
	for _, record := range state.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m, ok := f.image.Monitors[record.Intent.MonitorID]; ok {
			f.controls.ReplaceOrInsert(changeOf(m))
		}
	}
	return ctx.Err()
}
