package persistence

// updateIncidentDeliveryCounts preserves disposition totals when execution
// references retire. It compares only the command's working set, never history.
// An older incident without complete tracking stays explicitly unavailable.
func (f *machine) updateIncidentDeliveryCounts(previous, current Monitor) {
	if current.IncidentDeliveries == nil {
		return
	}
	before, after := make(map[string]Action), make(map[string]Action)
	add := func(dst map[string]Action, owner Monitor, a Action) {
		if a.Kind != "code" || a.IncidentID != current.IncidentID || !owner.OwnsAction(a) {
			return
		}
		id := a.DeliveryID
		if id == "" {
			id = a.ID
		}
		if old, exists := dst[id]; !exists || a.Attempt > old.Attempt {
			dst[id] = a
		}
	}
	counts := NotificationCounts{}
	if previous.IncidentID == current.IncidentID {
		counts = previous.IncidentNotifications()
		for _, a := range previous.Actions {
			add(before, previous, a)
		}
	}
	for _, actions := range []map[string]Action{current.retainedActions, current.Actions} {
		for _, a := range actions {
			add(after, current, a)
			if previous.IncidentID != current.IncidentID || a.Kind != "code" || a.IncidentID != current.IncidentID {
				continue
			}
			if _, exists := previous.Actions[a.ID]; exists {
				continue
			}
			// Exact review/evidence commands can address a retained record that
			// has already left the execution projection. It is not a new delivery.
			old, found, err := f.actionForMonitor(previous, a.ID)
			if err != nil {
				return
			}
			if found {
				add(before, previous, old)
			}
		}
	}
	for id, a := range after {
		if old, exists := before[id]; exists {
			counts.adjust(old.State, -1)
		}
		counts.adjust(a.State, 1)
	}
	// Clone() gives each transition its own pointer. The result and installed
	// image share this update until the submission result is detached for callers.
	*current.IncidentDeliveries = counts
}

func (c *NotificationCounts) adjust(state ActionState, delta int64) {
	switch state {
	case Queued, Started:
		c.Pending += delta
	case Succeeded:
		c.Accepted += delta
	case Failed:
		c.Failed += delta
	case Unknown:
		c.Unknown += delta
	case Cancelled:
		c.Cancelled += delta
	}
}
