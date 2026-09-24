package management

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func incidentView(record persistence.IncidentRecord) api.Incident {
	state := "closed"
	if record.Active {
		state = "open"
	}
	return api.Incident{ID: record.ID, MonitorID: record.MonitorID, Revision: record.Revision, State: state,
		OpenedAt: record.OpenedAt, ClosedAt: record.ClosedAt, AcknowledgedBy: record.AcknowledgedBy,
		AcknowledgedAt: record.AcknowledgedAt, Dismissed: record.Dismissed}
}

func incidentFromMonitor(m persistence.Monitor) api.Incident {
	return incidentView(persistence.IncidentRecord{ID: m.IncidentID, MonitorID: m.ID, MonitorUID: m.CatalogUID,
		Revision: m.IncidentRevision, Active: !m.Removed && m.IncidentClosedAt.IsZero() && (m.Incident || m.Recovering), OpenedAt: m.IncidentOpenedAt, ClosedAt: m.IncidentClosedAt,
		AcknowledgedBy: m.AcknowledgedBy, AcknowledgedAt: m.AcknowledgedAt, Dismissed: m.Dismissed})
}

func (c *Catalog) Incident(ctx context.Context, id string) (api.Incident, error) {
	if err := ctx.Err(); err != nil {
		return api.Incident{}, err
	}
	if !c.Ready() {
		return api.Incident{}, ErrUnavailable
	}
	if !validID(id) {
		return api.Incident{}, ErrValidation
	}
	record, ok, err := c.store.Incident(id)
	if err != nil {
		return api.Incident{}, err
	}
	if !ok {
		return api.Incident{}, persistence.ErrCatalogNotFound
	}
	return incidentView(record), nil
}

// IncidentView freezes the ordered latest-incident index. One compact record is
// retained per monitor; older incident events remain in retained history.
type IncidentView struct {
	view    persistence.IncidentView
	catalog *Catalog
}

func (c *Catalog) IncidentSnapshot() (IncidentView, error) {
	if !c.Ready() {
		return IncidentView{}, ErrUnavailable
	}
	view, err := c.store.IncidentSnapshot()
	return IncidentView{view: view, catalog: c}, err
}

func (v IncidentView) Page(ctx context.Context, monitorID, after string, limit int) ([]api.Incident, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if v.catalog == nil || !v.catalog.Ready() {
		return nil, "", ErrUnavailable
	}
	if limit < 1 || limit > 500 || (monitorID != "" && !validID(monitorID)) {
		return nil, "", ErrValidation
	}
	if monitorID != "" {
		items := []api.Incident{}
		record, ok := v.view.ForMonitor(monitorID)
		if ok && record.ID > after {
			items = append(items, incidentView(record))
		}
		return items, "", nil
	}
	records, next, err := v.view.Page(after, limit)
	if err != nil {
		return nil, "", err
	}
	items := make([]api.Incident, 0, len(records))
	for _, record := range records {
		items = append(items, incidentView(record))
	}
	return items, next, nil
}

// observedResource adds a bounded current observation to desired configuration.
// Resource pages freeze desired configuration, not health. A stale desired page
// must never acquire controls for a newer configuration or replacement monitor.
func (c *Catalog) observedResource(ctx context.Context, resource api.Resource) (api.Resource, error) {
	if ctx == nil {
		return api.Resource{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return api.Resource{}, err
	}
	resource = publicResource(resource)
	if resource.Kind != "Monitor" {
		return resource, nil
	}
	status := api.MonitorStatus{Health: "unknown", LastCheckLatencyMS: api.Measurement{Available: false, Reason: "configurationPending"}}
	current, ok, err := c.store.MonitorStatusContext(ctx, resource.Metadata.ID)
	if err != nil {
		return api.Resource{}, err
	}
	if ok && !current.Removed && current.CatalogUID == resource.Metadata.UID && current.CatalogRevision == resource.Metadata.ResourceVersion {
		status.ControlRevision = current.ControlRevision
		status.ExecutionRevision = current.Revision
		// This process-local marker is set only after actual owner installation.
		// It is never inferred from a committed configure or replayed snapshot.
		status.ObservedGeneration = int64(current.ObservedGeneration)
		status.StatusRevision = current.Revision + ":" + strconv.FormatUint(current.Generation, 10) + ":" + current.ControlRevision + ":" + current.IncidentRevision + ":" + strconv.FormatUint(current.ObservedGeneration, 10)
		status.IncidentID = current.IncidentID
		status.SnoozedUntil = current.SnoozedUntil
		status.LastCheckedAt = current.LastCheck
		status.UnknownActions = int64(current.UnknownActions)
		status.LastCheckLatencyMS = api.Measurement{Available: current.LatencyAvailable, Reason: "notObserved"}
		if current.LatencyAvailable {
			status.LastCheckLatencyMS.Reason = ""
			status.LastCheckLatencyMS.Value = float64(current.LastLatency) / float64(time.Millisecond)
		}
		if !current.LastCheck.IsZero() {
			switch current.LastOutcome {
			case "failure", "timeout":
				status.Health = "unhealthy"
			case "success":
				status.Health = "healthy"
				if current.Warning {
					status.Health = "warning"
				}
			}

		}
	}
	resource.Status, err = json.Marshal(status)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return api.Resource{}, ctxErr
	}
	return resource, err
}
