//go:build externaljobs

package persistence

// This index belongs to one validation pass over a frozen or privately decoded
// image. Runtime indexes are neither trusted nor changed during validation.
type workerActionValidation struct {
	byAction map[string]string
}

func newWorkerActionValidation(i image) (workerActionValidation, error) {
	var validation workerActionValidation
	if i.WorkerExecutions == nil {
		return validation, nil
	}
	if len(i.WorkerExecutions.Records) > MaxWorkerExecutions {
		return validation, ErrWorkerExecutionQuota
	}
	validation.byAction = make(map[string]string, len(i.WorkerExecutions.Records))
	for id, r := range i.WorkerExecutions.Records {
		if r.Intent.ActionID == "" {
			continue
		}
		if _, exists := validation.byAction[r.Intent.ActionID]; exists {
			return workerActionValidation{}, ErrWorkerExecutionConflict
		}
		validation.byAction[r.Intent.ActionID] = id
	}
	return validation, nil
}

func (v workerActionValidation) validAction(i image, a Action, archived bool) bool {
	if id, found := v.byAction[a.ID]; found {
		if i.Version < WorkerOfferFormatVersion || a.ExecutorKind != "worker" || a.State == Queued || a.State == Cancelled || !a.ExecutorFinishedAt.IsZero() {
			return false
		}
		r := i.WorkerExecutions.Records[id]
		l := r.Lifecycle
		if l == nil || r.Intent.ActionID != a.ID || r.Intent.MonitorUID != a.CatalogUID || r.Intent.MonitorRevision != a.Revision || l.GrantID != a.ExecutorSession || !l.StartedAt.Equal(a.StartedAt) {
			return false
		}
		if r.Result == nil {
			return (l.Phase == "started" || l.Phase == "unknown") && (a.State == Started || a.State == Unknown)
		}
		return i.Version >= WorkerResultFormatVersion && ((l.Phase == "unknown" && a.State == Unknown) ||
			(l.Phase == "finished" && ((a.State == Succeeded && a.Outcome == r.Result.Input.Status) || (a.State == Failed && r.Result.Input.Status == "rejected"))))
	}
	// Only absent, retired receipts permit this terminal-action fallback. A
	// present but contradictory receipt must fail the exact join above.
	if !archived || !externalStorageFormat(i.Version) || a.ExecutorKind != "worker" ||
		(a.State != Succeeded && a.State != Failed) || !catalogIdentifier(a.ExecutorSession, 256) ||
		a.StartedAt.IsZero() || a.FinishedAt.Before(a.StartedAt) || !a.ExecutorFinishedAt.IsZero() {
		return false
	}
	return (a.State == Failed && a.Outcome == "rejected") ||
		(a.State == Succeeded && (a.Outcome == "accepted" || a.Outcome == "completed"))
}

// Complete the deferred snapshot link check using exact action identities.
// Historical actions remain in the ledger rather than the live monitor map.
func validateWorkerActionDependencies(i image, view *collectionLedgerView) error {
	if i.WorkerExecutions == nil {
		return nil
	}
	for _, execution := range i.WorkerExecutions.Records {
		l := execution.Lifecycle
		if execution.Intent.Category == "check" || l == nil || l.GrantID == "" || !workerActionDependencyRequired(execution) {
			continue
		}
		m, ok := i.Monitors[execution.Intent.MonitorID]
		if !ok {
			return ErrWorkerExecutionInvalid
		}
		a, ok := m.Actions[execution.Intent.ActionID]
		if !ok {
			row, found, err := view.readActionHead(execution.Intent.ActionID)
			if err != nil || !found || row.MonitorID != m.ID {
				return ErrWorkerExecutionInvalid
			}
			a = row.Action
		}
		m = withAction(m, a)
		check := i
		check.Monitors = map[string]Monitor{m.ID: m}
		if err := validateWorkerExecutionLifecycle(check, execution); err != nil {
			return err
		}
	}
	return nil
}
