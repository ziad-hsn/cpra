//go:build externaljobs

package persistence

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
