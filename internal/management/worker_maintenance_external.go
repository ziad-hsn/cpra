//go:build externaljobs

package management

import (
	"context"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// EvaluateWorkerMaintenance evaluates only the immutable Monitor configuration
// pinned by this execution. No provider clients or dependency secrets are opened.
// A removed or superseded configuration has no permission for follow-up work,
// but must not prevent the original worker outcome or receipt from being kept.
func (c *Catalog) EvaluateWorkerMaintenance(ctx context.Context, in persistence.WorkerExecutionIntent, queued, at time.Time) (persistence.WorkerMaintenanceDecision, error) {
	var out persistence.WorkerMaintenanceDecision
	view, err := c.store.CatalogSnapshotContext(ctx)
	if err != nil {
		return out, err
	}
	key := persistence.CatalogKey{Kind: "Monitor", ID: in.MonitorID}
	record, exists := view.Get(key)
	if !exists || record.Removed || record.UID != in.MonitorUID {
		return out, nil
	}
	matched := false
	for _, condition := range in.Guard.Conditions {
		if condition.Key == key && condition.UID == record.UID && condition.Revision == record.Revision {
			matched = true
			break
		}
	}
	if !matched {
		m, found, err := c.store.GetContext(ctx, in.MonitorID)
		if err != nil {
			return out, err
		}
		a, actionFound := m.Actions[in.ActionID]
		if !found || !actionFound || a.Resolution == nil && a.ReviewBinding == "" || a.Revision != in.MonitorRevision || m.CatalogRevision != record.Revision || !m.ActionConfigurationCurrent(a) {
			return out, nil
		}
		out.CurrentMonitorRevision = record.Revision
	}
	resource, err := c.open(ctx, record)
	if err != nil {
		return out, err
	}
	var spec api.MonitorSpec
	if api.StrictDecode(resource.Spec, &spec) != nil {
		return out, ErrValidation
	}
	periodic, absolute, err := runtimeMaintenance(spec.Maintenance)
	if err != nil {
		return out, err
	}
	windows, err := manifest.CompileMaintenance(periodic)
	if err != nil {
		return out, err
	}
	out.Available = true
	out.Active = manifest.InMaintenance(windows, at)
	out.SinceQueued = manifest.MaintenanceBetween(windows, queued, at)
	for _, window := range windows {
		next := window.Schedule.Next(at)
		if !next.IsZero() && (out.Until.IsZero() || next.Before(out.Until)) {
			out.Until = next
		}
	}
	for _, w := range absolute {
		if !at.Before(w.Start) && at.Before(w.End) {
			out.Active = true
		}
		if !queued.IsZero() && !at.Before(queued) && !at.Before(w.Start) && queued.Before(w.End) {
			out.SinceQueued = true
		}
		if w.Start.After(at) && (out.Until.IsZero() || w.Start.Before(out.Until)) {
			out.Until = w.Start
		}
	}
	return out, ctx.Err()
}
