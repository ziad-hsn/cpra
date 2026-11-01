package management

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func incidentView(record persistence.IncidentRecord) api.Incident {
	state := "closed"
	if record.Active {
		state = "open"
	} else if record.ClosedAt.IsZero() {
		state = "pending"
	}
	result := api.Incident{ID: record.ID, MonitorID: record.MonitorID, Revision: record.Revision, State: state,
		FirstFailureAt: record.FirstFailureAt.UTC(), OpenedAt: record.OpenedAt.UTC(), ClosedAt: record.ClosedAt.UTC(), AcknowledgedBy: record.AcknowledgedBy,
		AcknowledgedAt: record.AcknowledgedAt.UTC(), Dismissed: record.Dismissed}
	if record.NotificationsAvailable {
		v := record.Notifications
		result.Notifications = &api.IncidentNotifications{Pending: v.Pending, Accepted: v.Accepted, Failed: v.Failed, Unknown: v.Unknown, Cancelled: v.Cancelled}
	}
	return result
}

func incidentFromMonitor(m persistence.Monitor) api.Incident {
	return incidentView(persistence.IncidentRecord{ID: m.IncidentID, MonitorID: m.ID, MonitorUID: m.CatalogUID,
		Revision: m.IncidentRevision, Active: m.IncidentActive(), FirstFailureAt: m.IncidentOpenedAt, OpenedAt: m.IncidentConfirmedAt, ClosedAt: m.IncidentClosedAt,
		AcknowledgedBy: m.AcknowledgedBy, AcknowledgedAt: m.AcknowledgedAt, Dismissed: m.Dismissed, Notifications: m.IncidentNotifications(), NotificationsAvailable: m.IncidentDeliveries != nil})
}

func (c *Catalog) Incident(ctx context.Context, id string) (api.Incident, error) {
	if err := ctx.Err(); err != nil {
		return api.Incident{}, err
	}
	if err := c.readyContext(ctx); err != nil {
		return api.Incident{}, err
	}
	if !validID(id) {
		return api.Incident{}, ErrValidation
	}
	record, ok, err := c.store.IncidentContext(ctx, id)
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
	view   persistence.IncidentView
	ready  func(context.Context) error
	filter *persistence.IncidentFilter
}

// WithFilter binds validated query values to this immutable snapshot.
func (v IncidentView) WithFilter(filter persistence.IncidentFilter) IncidentView {
	v.filter = &filter
	return v
}

func (c *Catalog) IncidentSnapshot() (IncidentView, error) {
	return c.IncidentSnapshotContext(context.Background())
}

func (c *Catalog) IncidentSnapshotContext(ctx context.Context) (IncidentView, error) {
	if err := c.readyContext(ctx); err != nil {
		return IncidentView{}, err
	}
	view, err := c.store.IncidentSnapshotContext(ctx)
	return IncidentView{view: view, ready: c.readyContext}, err
}

func (v IncidentView) Page(ctx context.Context, monitorID, after string, limit int) ([]api.Incident, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if v.ready == nil {
		return nil, "", ErrUnavailable
	}
	if err := v.ready(ctx); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 500 || (monitorID != "" && !validID(monitorID)) {
		return nil, "", ErrValidation
	}
	if monitorID != "" {
		items := []api.Incident{}
		record, ok := v.view.ForMonitor(monitorID)
		if ok && record.ID != after && (v.filter == nil || v.filter.Matches(record)) {
			items = append(items, incidentView(record))
		}
		return items, "", nil
	}
	var records []persistence.IncidentRecord
	var next string
	var err error
	if v.filter != nil {
		records, next, err = v.view.FilteredPage(ctx, after, limit, *v.filter)
	} else {
		records, next, err = v.view.Page(after, limit)
	}
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
	return c.observeResource(ctx, resource, false)
}

// observedResourceDetail adds a bounded retained-condition read only for a
// single-resource response. Paginated configuration reads remain memory-only.
func (c *Catalog) observedResourceDetail(ctx context.Context, resource api.Resource) (api.Resource, error) {
	return c.observeResource(ctx, resource, true)
}

func (c *Catalog) observeResource(ctx context.Context, resource api.Resource, detail bool) (api.Resource, error) {
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
	status.NotificationConditionsTruncated = detail
	status.Availability = api.MonitorAvailability{State: api.MonitorAvailabilityState("unavailable"), Reason: "controllerUnavailable"}
	warnings := api.ResourceWarnings(resource)
	if detail {
		warnings = c.ConfigurationWarnings(ctx, resource)
	}
	if len(warnings) > 0 {
		status.Warnings = &warnings
	}
	current, ok, err := c.store.MonitorStatusContext(ctx, resource.Metadata.ID)
	if err != nil {
		return api.Resource{}, err
	}
	if ok && !current.Removed && current.CatalogUID == resource.Metadata.UID && current.CatalogRevision == resource.Metadata.ResourceVersion {
		status.ControlRevision = current.ControlRevision
		status.RecoveryDecision = current.RecoveryDecision
		status.RecoveryAdmission = current.RecoveryAdmission
		status.NotificationConditionsTruncated = current.NotificationConditionsTruncated
		status.NotificationConditions = make([]api.NotificationCondition, 0, len(current.NotificationConditions))
		for _, condition := range current.NotificationConditions {
			status.NotificationConditions = append(status.NotificationConditions, notificationConditionView(condition))
		}
		if detail {
			view, found, readErr := c.store.NotificationConditionsContext(ctx, current.ID)
			if readErr != nil && !errors.Is(readErr, persistence.ErrNotificationObservationChanged) {
				return api.Resource{}, errors.Join(ErrUnavailable, readErr)
			}
			status.NotificationConditionsTruncated = true
			if readErr == nil && found && notificationConditionsMatchStatus(view, current) {
				status.NotificationConditions = make([]api.NotificationCondition, 0, len(view.Items))
				for _, condition := range view.Items {
					status.NotificationConditions = append(status.NotificationConditions, notificationConditionView(condition))
				}
				status.NotificationConditionsTruncated = view.Truncated
				at := view.ObservedAt.UTC()
				status.NotificationConditionsObservedAt = &at
				status.NotificationConditionsSequence = strconv.FormatUint(view.Sequence, 10)
			}
		}
		status.ExecutionRevision = current.Revision
		// This process-local marker is set only after actual owner installation.
		// It is never inferred from a committed configure or replayed snapshot.
		status.ObservedGeneration = int64(current.ObservedGeneration)
		status.StatusRevision = current.Revision + ":" + strconv.FormatUint(current.Generation, 10) + ":" + current.ControlRevision + ":" + current.IncidentRevision + ":" + strconv.FormatUint(current.ObservedGeneration, 10)
		status.IncidentID = current.IncidentID
		status.SnoozedUntil = current.SnoozedUntil.UTC()
		status.Ignored, status.IgnoredAt, status.IgnoredBy = current.Ignored, current.IgnoredAt.UTC(), current.IgnoredBy
		status.IgnoreCause, status.IgnoreReason, status.IgnoreIncidentCount = current.IgnoreCause, current.IgnoreReason, int64(current.IgnoreIncidentCount)
		status.LastCheckedAt = current.LastCheck.UTC()
		status.LastFailure = failureView(current.LastFailure)
		status.ConfigurationProblem = failureView(current.ConfigurationProblem)
		if !current.ConfigurationProblemAt.IsZero() {
			at := current.ConfigurationProblemAt.UTC()
			status.ConfigurationProblemAt = &at
		}
		status.UnknownActions = api.Pointer(int64(current.UnknownActions))
		status.ActionsNeedingReview = api.Pointer(int64(current.ActionsNeedingReview))
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
		status.LastObservedHealth = status.Health
		if !current.LastCheck.IsZero() {
			at := current.LastCheck.UTC()
			status.LastObservedAt = &at
		}
		// Durable health records are not proof that this running owner can
		// currently execute checks or has evaluated maintenance for this revision.
		status.Health = "unknown"
		if holder := c.observations.Load(); holder != nil && holder.Index() != nil {
			row, found := holder.Index().GetStable(resource.Metadata.ID)
			row = holder.Observation(row)
			if found && current.ObservedGeneration > 0 && current.ObservedGeneration == uint64(resource.Metadata.Generation) && row.CatalogRevision == resource.Metadata.ResourceVersion && row.IncarnationUID == current.CatalogUID && row.ConfigurationRevision == current.Revision && row.ControlRevision == current.ControlRevision && row.StateGeneration == current.Generation && row.IncidentID == current.IncidentID && row.IncidentRevision == current.IncidentRevision {
				status.Health = row.Health
				status.RecoveryDecision, status.RecoveryAdmission = row.RecoveryDecision, row.RecoveryAdmission
				status.Availability = api.MonitorAvailability{State: api.MonitorAvailabilityState(row.Availability.State), Reason: row.Availability.Reason}
				if !row.Availability.ObservedAt.IsZero() {
					at := row.Availability.ObservedAt.UTC()
					status.Availability.ObservedAt = &at
				}
				status.Maintenance = api.Pointer(row.Maintenance)
				status.NotificationsSuppressed = api.Pointer(row.NotificationsSuppressed)
				if !row.SuppressionObservedAt.IsZero() {
					at := row.SuppressionObservedAt.UTC()
					status.SuppressionObservedAt = &at
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

func notificationConditionsMatchStatus(view persistence.NotificationConditionView, status persistence.MonitorStatus) bool {
	return view.IncarnationID == status.CatalogUID && view.Revision == status.Revision &&
		view.CatalogRevision == status.CatalogRevision && view.ControlRevision == status.ControlRevision &&
		view.Generation == status.Generation && view.IncidentID == status.IncidentID && view.IncidentRevision == status.IncidentRevision
}

func notificationConditionView(condition persistence.NotificationConditionObservation) api.NotificationCondition {
	out := api.NotificationCondition{ConditionID: condition.ConditionID, Kind: condition.Kind, Color: condition.Color,
		State: condition.State, Reason: condition.Reason, OriginalActionID: condition.OriginalActionID, MigrationReason: condition.MigrationReason}
	if !condition.PendingAt.IsZero() {
		at := condition.PendingAt.UTC()
		out.PendingAt = &at
	}
	if !condition.RecoveredAt.IsZero() {
		at := condition.RecoveredAt.UTC()
		out.RecoveredAt = &at
	}
	return out
}

func failureView(d persistence.FailureDiagnostic) *api.FailureDiagnostic {
	if d.Code == "" {
		return nil
	}
	out := &api.FailureDiagnostic{Code: d.Code, Message: d.Message, Phase: d.Phase, TaxonomyVersion: d.TaxonomyVersion}
	if d.HTTPStatus != 0 {
		out.HTTPStatus = &d.HTTPStatus
	}
	if d.GRPCStatus != "" {
		status := api.FailureDiagnosticGrpcStatus(d.GRPCStatus)
		out.GRPCStatus = &status
	}
	if d.GRPCCode != "" {
		out.GRPCCode = &d.GRPCCode
	}
	if !d.CertificateNotAfter.IsZero() {
		at := d.CertificateNotAfter.UTC()
		out.CertificateNotAfter = &at
		out.CertificateDaysRemaining = &d.CertificateDaysRemaining
	}
	return out
}
