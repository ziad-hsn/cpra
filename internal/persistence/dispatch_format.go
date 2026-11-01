package persistence

// DispatchFormatVersion preserves pending notification ownership and starts new
// intervention budgets only after execution capacity is reserved. Historical
// envelopes keep the timing and ownership semantics of their recorded format.
const DispatchFormatVersion = 50
const dispatchSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-50\n"

func dispatchStorageFormat(version int) bool {
	return version == DispatchFormatVersion || version == 51 && externalStorageFormat(version)
}

func actionStateVersion(version int) bool {
	return version == ActionFormatVersion || version == DispatchFormatVersion
}

func (m *Monitor) upgradeDispatch(version int) {
	if dispatchStorageFormat(version) {
		m.ActionVersion = DispatchFormatVersion
	}
}

func (m Monitor) waitingRecoveryBudget(a Action) bool {
	return m.ActionVersion == DispatchFormatVersion && a.Kind == "intervention" && a.ExecutionBudget > 0 && a.Deadline.IsZero() && a.StartedAt.IsZero()
}
