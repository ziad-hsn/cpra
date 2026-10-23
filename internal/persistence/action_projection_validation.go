package persistence

import (
	"fmt"
	"reflect"
)

func validateActionProjection(m Monitor, version int) error {
	if adopted := m.AdoptedFileIncarnation; adopted != nil && (!actionStorageFormat(version) || m.CatalogUID == "" || *adopted != m.FileIncarnation || *adopted != "" && !catalogIdentifier(*adopted, 256)) {
		return errActionLedger
	}
	p := m.ActionProjection
	if p == nil {
		return nil
	}
	if !actionStorageFormat(version) || p.Version != actionProjectionVersion || p.Unknown < 0 || p.Held < 0 || p.Held > p.Unknown || p.RecoveryHeld < 0 || p.RecoveryHeld > p.Held || p.ReviewConditions < 0 || len(m.ReviewAssessmentKey) > 2048 {
		return errActionLedger
	}
	for _, n := range []int64{p.Notifications.Pending, p.Notifications.Accepted, p.Notifications.Failed, p.Notifications.Unknown, p.Notifications.Cancelled} {
		if n < 0 {
			return errActionLedger
		}
	}
	if p.LatestRecovery != nil && (!m.OwnsAction(*p.LatestRecovery) || p.LatestRecovery.Kind != "intervention" || p.LatestRecovery.IncidentID != "" && p.LatestRecovery.IncidentID != m.IncidentID) {
		return errActionLedger
	}
	blue := 0
	for _, a := range m.Actions {
		if a.State != Queued && a.State != Started {
			return errActionLedger
		}
		if a.ReviewNotice != nil {
			blue++
		}
	}
	if p.PreviousReviewCapacity != 0 && (p.PreviousReviewCapacity <= activeReviewDeliveries || p.PreviousReviewCapacity != blue) || blue > max(activeReviewDeliveries, p.PreviousReviewCapacity) || m.Notifications != nil && len(m.Notifications.ReviewAlerts) != 0 {
		return errActionLedger
	}
	return nil
}
func (v *collectionLedgerView) readActionHead(id string) (actionLedgerRow, bool, error) {
	var data []byte
	if v.tx != nil {
		if b := v.tx.Bucket(actionCurrentBucket); b != nil {
			data = b.Get([]byte(id))
		}
	} else if v.actions != nil {
		data = v.actions.current[id]
	}
	if data == nil {
		return actionLedgerRow{}, false, nil
	}
	row, err := decodeActionRow(data)
	return row, !row.Deleted && err == nil, err
}
func (v *collectionLedgerView) readReviewHead(key string) (reviewRecord, bool, error) {
	var data []byte
	if v.tx != nil {
		if b := v.tx.Bucket(reviewRowsBucket); b != nil {
			data = b.Get([]byte(key))
		}
	} else if v.actions != nil {
		data = v.actions.reviews[key]
	}
	if data == nil {
		return reviewRecord{}, false, nil
	}
	row, err := decodeReviewRecord(data)
	if err == nil && row.Key != key {
		return reviewRecord{}, false, errActionLedger
	}
	return row, !row.Deleted && err == nil, err
}
func (v *collectionLedgerView) reviewValidationMonitor(m Monitor, alert ReviewAlert) (Monitor, error) {
	// Rebuild only this condition's bounded dependency set, never the history map.
	m.Actions = make(map[string]Action)
	m.Notifications = m.Notifications.clone()
	if m.Notifications == nil {
		m.Notifications = &NotificationState{}
	}
	m.Notifications.ReviewAlerts = map[string]ReviewAlert{alert.ID: alert}
	row, ok, err := v.readActionHead(alert.ActionID)
	if err != nil || !ok || row.MonitorID != m.ID {
		return m, errActionLedger
	}
	m.Actions[row.Action.ID] = row.Action
	for _, id := range []string{alert.OpeningDelivery, alert.ClosingDelivery} {
		if id == "" {
			continue
		}
		for n := 1; n <= 3; n++ {
			key := id
			if n > 1 {
				key = identity(fmt.Sprintf("%s/attempt/%d", id, n))
			}
			row, ok, err := v.readActionHead(key)
			if err != nil {
				return m, err
			}
			if ok {
				if row.Deleted || row.MonitorID != m.ID {
					return m, errActionLedger
				}
				m.Actions[key] = row.Action
			}
		}
	}
	return m, nil
}
func validateProjectionInventory(i image, v *collectionLedgerView, worker workerActionValidation) error {
	counts := make(map[string]ActionProjection)
	if err := v.walkActions(func(_ []byte, row actionLedgerRow) error {
		if row.Deleted {
			return nil
		}
		m := i.Monitors[row.MonitorID]
		if m.ActionProjection == nil {
			return nil
		}
		p := counts[m.ID]
		p.include(m, row.Action)
		counts[m.ID] = p
		return nil
	}); err != nil {
		return err
	}
	if err := v.walkReviews(func(_ []byte, r reviewRecord) error {
		m, ok := i.Monitors[r.MonitorID]
		if !ok {
			return errActionLedger
		}
		if r.Deleted {
			return nil
		}
		if r.Policy != nil {
			return nil // Referenced immutable policies are joined below at their captured identities.
		}
		if r.Alert != nil {
			p := counts[m.ID]
			p.ReviewConditions++
			counts[m.ID] = p
			input, err := v.reviewValidationMonitor(m, *r.Alert)
			if err != nil {
				return err
			}
			return input.validateReviewAlert(*r.Alert)
		}
		fact := r.Fact
		if fact == nil || fact.Fence > i.ActionStorage.Sequence || fact.Context.Policy.Validate() != nil {
			return errActionLedger
		}
		if err := v.validateReviewPolicy(fact.Context, fact.PolicyID, fact.Fence); err != nil {
			return err
		}
		header, _, err := v.readReviewHead(reviewPolicyKey(m.ID, fact.PolicyID, -1))
		if err != nil {
			return err
		}
		context, err := hydrateReviewRevision(fact.Context, fact.PolicyID, fact.Fence, header)
		if err != nil {
			return err
		}
		validate := func(id string, required bool) error {
			row, found, err := readActionVersion(v.tx, v.actions, id, fact.Fence)
			if err != nil || required && !found || found && row.MonitorID != m.ID {
				return errActionLedger
			}
			if !found {
				return nil
			} // This action was created after the observation.
			// Only intervention facts affect review eligibility. Other rows still
			// need a complete chain and immutable monitor identity at the fence.
			if row.Action.Kind == "intervention" && row.Action.State == Unknown {
				return validateRetainedAction(i, context, row.Action, true, worker)
			}
			if row.Action.Kind != "intervention" && row.Action.Kind != "code" || row.Action.Revision == "" {
				return errActionLedger
			}
			return nil
		}
		if fact.SourceAction != "" {
			return validate(fact.SourceAction, true)
		}
		return v.walkActions(func(_ []byte, row actionLedgerRow) error {
			if row.MonitorID != m.ID {
				return nil
			}
			return validate(row.Action.ID, false)
		})
	}); err != nil {
		return err
	}
	for id, m := range i.Monitors {
		if m.ReviewPolicyID == "" && m.ReviewRevisionDigest != "" {
			return errActionLedger
		}
		if m.ReviewPolicyID != "" {
			if reviewPolicyIdentity(m) != m.ReviewPolicyID || identity(m.Revision) != m.ReviewRevisionDigest {
				return errActionLedger
			}
			if err := v.validateReviewPolicy(m, m.ReviewPolicyID, i.ActionStorage.Sequence); err != nil {
				return err
			}
		}
		if p := m.ActionProjection; p != nil {
			actual := counts[id]
			if !reflect.DeepEqual(p, &ActionProjection{Version: actionProjectionVersion, PreviousReviewCapacity: p.PreviousReviewCapacity, Notifications: actual.Notifications, ReviewConditions: actual.ReviewConditions, Unknown: actual.Unknown, Held: actual.Held, RecoveryHeld: actual.RecoveryHeld, LatestRecovery: actual.LatestRecovery, LastFailure: actual.LastFailure, RejectedAt: actual.RejectedAt}) {
				return errActionLedger
			}
		}
	}
	return nil
}
