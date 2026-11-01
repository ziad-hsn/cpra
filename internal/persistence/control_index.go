package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/google/btree"
)

const controlChangeCapacity = 8192

var ErrControlChangeGap = errors.New("control change cursor expired; rebuild from a fresh control view")

type ControlCursor struct {
	owner           *machine
	epoch, position uint64
}

func (ControlCursor) MarshalJSON() ([]byte, error) {
	return nil, errors.New("process-local control cursor cannot be serialized")
}

type ControlChange struct {
	MonitorID, MonitorUID, ControlRevision, IncidentID, IncidentRevision string
	SnoozedUntil                                                         time.Time
	Enabled, Removed                                                     bool
}
type ControlChanges struct {
	Changes []ControlChange
	Next    ControlCursor
	More    bool
}
type ControlView struct {
	tree   *btree.BTreeG[ControlChange]
	Cursor ControlCursor
	Index  uint64
}

func changeOf(m Monitor) ControlChange {
	return ControlChange{MonitorID: m.ID, MonitorUID: m.CatalogUID, ControlRevision: m.ControlRevision, IncidentID: m.IncidentID, IncidentRevision: m.IncidentRevision, SnoozedUntil: m.SnoozedUntil, Enabled: m.Policy.Enabled, Removed: m.Removed}
}
func incidentOf(m Monitor) IncidentRecord {
	return IncidentRecord{ID: m.IncidentID, MonitorID: m.ID, MonitorUID: m.CatalogUID, Revision: m.IncidentRevision, Active: m.IncidentActive(), FirstFailureAt: m.IncidentOpenedAt, OpenedAt: m.IncidentConfirmedAt, ClosedAt: m.IncidentClosedAt, AcknowledgedBy: m.AcknowledgedBy, AcknowledgedAt: m.AcknowledgedAt, AcknowledgedNote: m.AcknowledgedNote, Dismissed: m.Dismissed, DismissedBy: m.DismissedBy, DismissedAt: m.DismissedAt, DismissedReason: m.DismissedReason, Notifications: m.incidentNotifications, NotificationsAvailable: m.IncidentDeliveries != nil}
}
func statusOf(m Monitor) MonitorStatus {
	conditions, truncated := m.NotificationConditions()
	return MonitorStatus{RecoveryDecision: m.RuleObservation(time.Now().UTC()), RecoveryAdmission: m.RecoveryAdmissionObservation(time.Now().UTC()), NotificationConditions: conditions, NotificationConditionsTruncated: truncated, ConfigurationProblem: m.ConfigurationProblem, ConfigurationProblemAt: m.ConfigurationProblemAt, Ignored: m.Ignored, IgnoredAt: m.IgnoredAt, IgnoredBy: m.IgnoredBy, IgnoreCause: m.IgnoreCause, IgnoreReason: m.IgnoreReason, IgnoreIncidentCount: len(m.IgnoreIncidents), ObservedGeneration: m.ownerObserved.generation, ID: m.ID, CatalogUID: m.CatalogUID, CatalogRevision: m.CatalogRevision, Revision: m.Revision, ControlRevision: m.ControlRevision, IncidentID: m.IncidentID, IncidentRevision: m.IncidentRevision, SnoozedUntil: m.SnoozedUntil, LastCheck: m.LastCheck, LastSuccess: m.LastSuccess, NextCheck: m.NextCheck, Enabled: m.Policy.Enabled, Removed: m.Removed, Incident: m.Incident, Recovering: m.Recovering, Warning: m.Warning, LatencyAvailable: m.LatencyAvailable, Generation: m.Generation, TotalChecks: m.TotalChecks, SuccessfulChecks: m.SuccessfulChecks, LastOutcome: m.LastOutcome, LastFailure: m.LastFailure, LastLatency: m.LastLatency, UnknownActions: m.unknownActions, ActionsNeedingReview: m.heldActions}
}
func (f *machine) emptyControlIndexes() {
	f.incidents = btree.NewG(32, func(a, b IncidentRecord) bool { return a.ID < b.ID })
	f.incidentsByMonitor = btree.NewG(32, func(a, b IncidentRecord) bool { return a.MonitorID < b.MonitorID })
	f.incidentsByTriage = btree.NewG(32, incidentTriageLess)
	f.controls = btree.NewG(32, func(a, b ControlChange) bool { return a.MonitorID < b.MonitorID })
}
func (f *machine) indexMonitor(m Monitor) {
	if m.IncidentID != "" {
		r := incidentOf(m)
		f.incidents.ReplaceOrInsert(r)
		f.incidentsByMonitor.ReplaceOrInsert(r)
		f.incidentsByTriage.ReplaceOrInsert(r)
	}
	if m.IncidentID != "" || !m.SnoozedUntil.IsZero() || m.ControlRevision != identity("control/"+m.ID+"/"+m.incarnationIdentity()) || f.hasWorkerCheckState(m) {
		f.controls.ReplaceOrInsert(changeOf(m))
	}
}
func (f *machine) rebuildControlIndexes() {
	_ = f.rebuildControlIndexesContext(context.Background())
}

func (f *machine) rebuildControlIndexesContext(ctx context.Context) (err error) {
	f.emptyControlIndexes()
	f.recoveryPrograms = recoveryProgramCache{}
	defer func() {
		if err != nil {
			f.incidents, f.incidentsByMonitor, f.controls = nil, nil, nil
			f.incidentsByTriage = nil
		}
	}()
	for id, m := range f.image.Monitors {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.ensureControlIdentity()
		m.Policy.RecoveryRules = f.recoveryPrograms.replace(RecoveryRules{}, m.Policy.RecoveryRules)
		m.unknownActions, m.heldActions = 0, 0
		if m.ActionProjection != nil {
			m.unknownActions, m.heldActions = m.ActionProjection.Unknown, m.ActionProjection.Held
		}
		m.incidentNotifications = m.IncidentNotifications()
		for _, a := range m.Actions {
			if err := ctx.Err(); err != nil {
				return err
			}
			if m.ActionProjection == nil && a.Held() {
				m.heldActions++
			}
			if m.ActionProjection == nil && a.State == Unknown {
				m.unknownActions++
			}
		}
		f.image.Monitors[id] = m
		f.indexMonitor(m)
	}
	return f.indexExtensionMonitors(ctx)
}

// installMonitor runs only in Apply. Trees contain immutable scalar projections;
// readers never walk mutable action maps or scan the fleet for an incident page.
func (f *machine) installMonitor(previous, m Monitor, at time.Time) []Event {
	f.updateIncidentDeliveryCounts(previous, m)
	m = m.Clone()
	f.recordActionWrites(previous, m)
	f.recordReviewWork(previous, &m, at)
	f.projectActions(previous, &m)
	f.indexProjectionReviews(previous, m)
	m.reviewAssessment = false
	m.retainedActions = nil
	f.image.Version = joinStorageFormat(f.image.Version, policyMinimumFormat(m.Policy))
	f.image.Version = joinStorageFormat(f.image.Version, recoveryNotificationPolicyFormat(m.Policy))
	preserveOwnerObservation(previous, &m)
	if f.incidents == nil {
		f.rebuildControlIndexes()
	}
	var events []Event
	for _, subject := range []string{"control", "incident"} {
		oldVersion := previous.ControlRevision
		if subject == "incident" {
			oldVersion = previous.IncidentRevision
		}
		receipt, ok := f.operationByVersion(CatalogKey{Kind: "Monitor", ID: previous.ID}, previous.CatalogUID, subject, oldVersion)
		if !ok || receipt.Subject == "" {
			continue
		}
		unchanged := previous.CatalogUID == m.CatalogUID && !m.Removed
		if receipt.Subject == "control" {
			unchanged = unchanged && receipt.NewVersion == m.ControlRevision
		} else {
			unchanged = unchanged && receipt.NewVersion == m.IncidentRevision && receipt.IncidentID == m.IncidentID && incidentOf(m).Active
		}
		if !unchanged {
			receipt.State, receipt.Outcome = "partial", "superseded"
			receipt.UpdatedAt = at
			if receipt.UpdatedAt.Before(receipt.At) {
				receipt.UpdatedAt = receipt.At
			}
			f.deleteOperation(receipt.ID)
			events = append(events, receiptEvent(receipt))
		}
	}
	// Cache the count at commit time so observation requests never scan actions.
	m.unknownActions, m.heldActions = 0, 0
	if m.ActionProjection != nil {
		m.unknownActions, m.heldActions = m.ActionProjection.Unknown, m.ActionProjection.Held
	}
	m.incidentNotifications = m.IncidentNotifications()
	for _, a := range m.Actions {
		if m.ActionProjection == nil && a.Held() {
			m.heldActions++
		}
		if m.ActionProjection == nil && a.State == Unknown {
			m.unknownActions++
		}
	}
	m.Policy.RecoveryRules = f.recoveryPrograms.replace(previous.Policy.RecoveryRules, m.Policy.RecoveryRules)
	f.image.Monitors[m.ID] = m
	f.updateActionIndexes(previous, m)
	events = append(events, f.supersedeActionReceipts(previous, at)...)
	oldIncident, newIncident := incidentOf(previous), incidentOf(m)
	if oldIncident != newIncident {
		if previous.IncidentID != "" {
			f.incidents.Delete(IncidentRecord{ID: previous.IncidentID})
			f.incidentsByMonitor.Delete(IncidentRecord{MonitorID: previous.ID})
			f.incidentsByTriage.Delete(oldIncident)
		}
		if m.IncidentID != "" {
			f.incidents.ReplaceOrInsert(newIncident)
			f.incidentsByMonitor.ReplaceOrInsert(newIncident)
			f.incidentsByTriage.ReplaceOrInsert(newIncident)
		}
	}
	if changeOf(previous) != changeOf(m) || oldIncident.Active != newIncident.Active {
		f.controls.Delete(ControlChange{MonitorID: m.ID})
		f.indexMonitor(m)
		if previous.ID != "" {
			f.publishControlChange(m)
		}
	}
	return events
}

func (f *machine) publishControlChange(m Monitor) {
	if f.controlChanges == nil {
		f.controlChanges = make([]ControlChange, controlChangeCapacity)
	}
	f.controlSequence++
	f.controlChanges[(f.controlSequence-1)%controlChangeCapacity] = changeOf(m)
}

func (s *Store) controlReadReady() error {
	if err := s.ControllerHealth(); err != nil {
		return errors.Join(errors.New("durable controls unavailable"), err)
	}
	return nil
}
func (s *Store) MonitorStatus(id string) (MonitorStatus, bool, error) {
	return s.MonitorStatusContext(context.Background(), id)
}

// MonitorStatusContext reads current status and bounds any owner-marker refresh
// by ctx. The ordinary cached path takes only the shared state lock.
func (s *Store) MonitorStatusContext(ctx context.Context, id string) (MonitorStatus, bool, error) {
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return MonitorStatus{}, false, err
	}
	f := s.fsm
	m, ok := f.image.Monitors[id]
	if !ok || m.ownerObserved.generation == 0 || m.ownerObserved.catalogSequence == f.catalogSequence {
		result := statusOf(m)
		unlock()
		if err := ctx.Err(); err != nil {
			return MonitorStatus{}, false, err
		}
		return result, ok, nil
	}
	unlock()
	// Recheck after acquiring exclusive ownership; a concurrent configuration
	// commit or owner mark may have replaced the record in the meantime.
	unlock, err = s.lockControllerState(ctx, true)
	if err != nil {
		return MonitorStatus{}, false, err
	}
	defer unlock()
	f = s.fsm
	m, ok = f.image.Monitors[id]
	if !ok {
		return MonitorStatus{}, false, ctx.Err()
	}
	m, err = f.refreshOwnerObservationContext(ctx, m)
	if err != nil {
		return MonitorStatus{}, false, err
	}
	return statusOf(m), true, nil
}

// IncidentView owns an immutable generation. A view can be shared by readers.
type IncidentView struct {
	tree, byMonitor *btree.BTreeG[IncidentRecord]
	triage          *btree.BTreeG[IncidentRecord]
	Index           uint64
}

func (s *Store) IncidentSnapshot() (IncidentView, error) {
	return s.IncidentSnapshotContext(context.Background())
}

func (s *Store) IncidentSnapshotContext(ctx context.Context) (IncidentView, error) {
	unlock, err := s.lockControllerState(ctx, true)
	if err != nil {
		return IncidentView{}, err
	}
	defer unlock()
	f := s.fsm
	if f.incidents == nil {
		if err := f.rebuildControlIndexesContext(ctx); err != nil {
			return IncidentView{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return IncidentView{}, err
	}
	return IncidentView{tree: f.incidents.Clone(), byMonitor: f.incidentsByMonitor.Clone(), triage: f.incidentsByTriage.Clone(), Index: f.image.Index}, nil
}
func (v IncidentView) Get(id string) (IncidentRecord, bool) {
	if v.tree == nil {
		return IncidentRecord{}, false
	}
	return v.tree.Get(IncidentRecord{ID: id})
}
func (v IncidentView) ForMonitor(id string) (IncidentRecord, bool) {
	if v.byMonitor == nil {
		return IncidentRecord{}, false
	}
	return v.byMonitor.Get(IncidentRecord{MonitorID: id})
}
func (v IncidentView) Page(after string, limit int) ([]IncidentRecord, string, error) {
	if v.tree == nil || limit < 1 || limit > 500 || (after != "" && !catalogIdentifier(after, 256)) {
		return nil, "", errors.New("invalid incident page")
	}
	records := make([]IncidentRecord, 0, limit)
	next := ""
	v.tree.AscendGreaterOrEqual(IncidentRecord{ID: after}, func(r IncidentRecord) bool {
		if r.ID == after {
			return true
		}
		if len(records) == limit {
			next = records[len(records)-1].ID
			return false
		}
		records = append(records, r)
		return true
	})
	return records, next, nil
}
func (s *Store) Incident(id string) (IncidentRecord, bool, error) {
	return s.IncidentContext(context.Background(), id)
}

func (s *Store) IncidentContext(ctx context.Context, id string) (IncidentRecord, bool, error) {
	// Snapshot cloning is O(1); no iteration or monitor/action copy is necessary.
	v, err := s.IncidentSnapshotContext(ctx)
	if err != nil {
		return IncidentRecord{}, false, err
	}
	r, ok := v.Get(id)
	if err := ctx.Err(); err != nil {
		return IncidentRecord{}, false, err
	}
	return r, ok, nil
}
func (s *Store) ControlSnapshot() (ControlView, error) {
	return s.ControlSnapshotContext(context.Background())
}

// ControlSnapshotContext captures the current control index within ctx.
func (s *Store) ControlSnapshotContext(ctx context.Context) (ControlView, error) {
	unlock, err := s.lockControllerState(ctx, true)
	if err != nil {
		return ControlView{}, err
	}
	defer unlock()
	f := s.fsm
	if f.controls == nil {
		if err := f.rebuildControlIndexesContext(ctx); err != nil {
			return ControlView{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return ControlView{}, err
	}
	return ControlView{tree: f.controls.Clone(), Index: f.image.Index, Cursor: ControlCursor{owner: f, epoch: f.controlEpoch, position: f.controlSequence}}, nil
}
func (v ControlView) Page(after string, limit int) ([]ControlChange, string, error) {
	if v.tree == nil || limit < 1 || limit > 500 || (after != "" && !catalogIdentifier(after, 256)) {
		return nil, "", errors.New("invalid control page")
	}
	rows := make([]ControlChange, 0, limit)
	next := ""
	v.tree.AscendGreaterOrEqual(ControlChange{MonitorID: after}, func(r ControlChange) bool {
		if r.MonitorID == after {
			return true
		}
		if len(rows) == limit {
			next = rows[len(rows)-1].MonitorID
			return false
		}
		rows = append(rows, r)
		return true
	})
	return rows, next, nil
}
func (s *Store) ControlsChangedSince(cursor ControlCursor, limit int) (ControlChanges, error) {
	return s.ControlsChangedSinceContext(context.Background(), cursor, limit)
}

// ControlsChangedSinceContext reads one bounded control-change page within ctx.
func (s *Store) ControlsChangedSinceContext(ctx context.Context, cursor ControlCursor, limit int) (ControlChanges, error) {
	if limit < 1 || limit > 500 {
		return ControlChanges{}, errors.New("invalid control change page")
	}
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return ControlChanges{}, err
	}
	defer unlock()
	f := s.fsm
	if cursor.owner != f || cursor.epoch != f.controlEpoch || cursor.position > f.controlSequence || f.controlSequence-cursor.position > controlChangeCapacity {
		return ControlChanges{}, ErrControlChangeGap
	}
	count := min(uint64(limit), f.controlSequence-cursor.position)
	r := ControlChanges{Next: cursor, More: count < f.controlSequence-cursor.position, Changes: make([]ControlChange, 0, count)}
	for range count {
		r.Changes = append(r.Changes, f.controlChanges[r.Next.position%controlChangeCapacity])
		r.Next.position++
	}
	if err := ctx.Err(); err != nil {
		return ControlChanges{}, err
	}
	return r, nil
}
