//go:build externaljobs

package persistence

import "time"

func (f *machine) workerResultTransition(r WorkerExecutionRecord, input WorkerResultInput, at time.Time, version int, decisions ...*WorkerMaintenanceDecision) (*Monitor, []Event, bool, bool, bool) {
	in, l := r.Intent, r.Lifecycle
	var decision *WorkerMaintenanceDecision
	if len(decisions) > 0 {
		decision = decisions[0]
	}
	maintenance := lifecycleStorageFormat(version) && (!decision.matches(in, at) || decision.Active || f.checkCatalogGuard(in.MonitorID, &in.Guard) != nil)
	if actionStorageFormat(version) && in.Category == "check" && decision != nil {
		maintenance = maintenance || decision.SinceQueued
	}
	previous, exists := f.image.Monitors[in.MonitorID]
	current := exists && !previous.Removed && previous.CatalogUID == in.MonitorUID && previous.Revision == in.MonitorRevision
	inTime := l.OwnerEpoch == f.image.LocalExecutorSession && l.Phase == "started" && !r.executionExpired(at)
	if in.Category == "check" {
		noData := !current || !inTime || input.Status == "noData" || in.Generation <= previous.Generation
		if !current || in.Generation <= previous.Generation {
			return nil, nil, false, true, false
		}
		if noData {
			m := previous.Clone()
			// Seal the missing generation without inventing a health observation or
			// allowing its delayed result to overwrite a newer check.
			m.Generation = in.Generation
			if next := in.Scheduled.Add(m.Policy.Interval); next.After(m.NextCheck) {
				m.NextCheck = next
			}
			return &m, nil, false, true, false
		}
		command := Command{Kind: "pulse", MonitorID: in.MonitorID, Revision: in.MonitorRevision,
			Generation: in.Generation, CheckControlRevision: in.ControlRevision, Outcome: input.Status, At: at, Maintenance: maintenance}
		if deliveryStorageFormat(version) && input.Status != "success" {
			command.Failure = NewFailureDiagnostic("operation_failed")
		}
		// The protocol does not report trustworthy handler timing. Retain actual
		// health without fabricating execution latency from network/result delay.
		// A control change still suppresses lifecycle side effects in transition.
		command.ExecutionStart = l.StartedAt
		result := transitionAtFormat(previous, command, version)
		if result.Monitor == nil || result.Err != nil {
			return nil, nil, false, true, false
		}
		result.Monitor.LastLatency, result.Monitor.LatencyAvailable = 0, false
		return result.Monitor, result.Events, true, false, false
	}
	var err error
	previous, err = f.hydrateReviewDependencies(previous, in.ActionID)
	if err != nil {
		f.err = err
		return nil, nil, false, false, true
	}
	action, found := previous.Actions[in.ActionID]
	if !exists || !found || action.CatalogUID != in.MonitorUID || action.Revision != in.MonitorRevision ||
		action.ExecutorKind != "worker" || action.ExecutorSession != l.GrantID {
		return nil, nil, false, false, true
	}
	if !current || !inTime || action.State != Started {
		if action.State != Started {
			return nil, nil, false, false, true
		}
		m := previous.Clone()
		action.State, action.Outcome, action.FinishedAt = Unknown, "worker_outcome_unknown", at
		if m.notificationConditionsEnabled() {
			action.TerminationReason = "outcome_unknown"
		}
		m.Actions[action.ID] = action
		m.recordNotificationCondition(action)
		event := Event{MonitorID: m.ID, CatalogUID: action.CatalogUID, Revision: action.Revision, At: at,
			Type: "action_unknown", IncidentID: action.IncidentID, ActionID: action.ID, Kind: action.Kind,
			Color: action.Color, Endpoint: action.Endpoint, Outcome: action.Outcome, Resolution: action.Resolution, TerminationReason: action.TerminationReason, DeliveryID: action.DeliveryID, NotificationEpisode: action.NotificationEpisode, EndpointID: action.EndpointID}
		events := append([]Event{event}, m.assessReviewAlertEvents(at)...)
		return &m, events, false, false, true
	}
	outcome := "success"
	if input.Status == "rejected" || input.Status == "unknown" {
		outcome = "failure"
	}
	command := Command{Kind: "result", MonitorID: in.MonitorID, Revision: in.MonitorRevision,
		ActionID: in.ActionID, At: at, Outcome: outcome, Retryable: input.Retryable, Ambiguous: input.Status == "unknown", Maintenance: maintenance}
	if deliveryStorageFormat(version) && outcome != "success" {
		code := "operation_failed"
		if input.Status == "rejected" {
			code = "rejected"
		}
		command.Failure = NewFailureDiagnostic(code)
	}
	result := transitionAtFormat(previous, command, version)
	if result.Monitor == nil || result.Err != nil {
		return nil, nil, false, false, true
	}
	if saved, ok := result.Monitor.Actions[in.ActionID]; ok && saved.State == Succeeded {
		saved.Outcome = input.Status
		result.Monitor.Actions[in.ActionID] = saved
		for n := range result.Events {
			if result.Events[n].ActionID == in.ActionID && result.Events[n].Type == "action_succeeded" {
				result.Events[n].Outcome = input.Status
			}
		}
	}
	return result.Monitor, result.Events, true, false, input.Status == "unknown"
}

func validateWorkerResultRecord(i image, r WorkerExecutionRecord) error {
	v, l := r.Result, r.Lifecycle
	if v == nil {
		return nil
	}
	if i.Version < WorkerResultFormatVersion || l == nil || l.GrantID == "" || v.Input.validate() != nil ||
		v.Input.ServerID != l.ServerID || v.Input.ExecutionID != r.Intent.ID || v.Input.ExecutionRevision != r.Intent.Revision || v.Input.GrantID != l.GrantID ||
		!workerResultStatus(v.Input.Status, r.Intent.Category) || v.RecordedAt.Before(l.StartedAt) || !v.RecordedAt.Equal(l.UpdatedAt) ||
		v.CommittedIndex < r.CommittedIndex || v.CommittedIndex > i.Index || v.CommittedIndex == 0 ||
		v.Receipt.ServerID != l.ServerID || v.Receipt.ExecutionID != r.Intent.ID || v.Receipt.WorkerUID != l.WorkerUID ||
		v.Receipt.ReceiptID != workerResultReceiptID(l.ServerID, l.GrantID, v.CommittedIndex) {
		return ErrWorkerExecutionInvalid
	}
	if _, err := workerResultRecordCost(v); err != nil {
		return err
	}
	if r.Intent.Category == "check" {
		disposition := "accepted"
		if v.NoData {
			disposition = "finalized"
		}
		if l.Phase != "finished" || v.NoData == v.Applied || v.Input.Status == "noData" && !v.NoData || v.Receipt.Disposition != disposition {
			return ErrWorkerExecutionInvalid
		}
	} else {
		if v.NoData || (v.Receipt.Disposition != "accepted" && v.Receipt.Disposition != "unknown") ||
			(v.Receipt.Disposition == "accepted" && (!v.Applied || l.Phase != "finished" || v.Input.Status == "unknown")) ||
			(v.Receipt.Disposition == "unknown" && l.Phase != "unknown") {
			return ErrWorkerExecutionInvalid
		}
	}
	return nil
}
