package persistence

// A bounded secondary index keeps configuration/control/review versions distinct
// from operation handles without scanning every pending operation per check.
type operationVersionKey struct {
	Key                    CatalogKey
	UID, Subject, Revision string
}

func operationVersion(r OperationReceipt) operationVersionKey {
	return operationVersionKey{Key: r.Key, UID: r.UID, Subject: r.Subject, Revision: r.NewVersion}
}
func (f *machine) rebuildOperationIndex() {
	f.operationVersions = make(map[operationVersionKey]string, len(f.image.Operations))
	for id, r := range f.image.Operations {
		f.operationVersions[operationVersion(r)] = id
	}
}
func (f *machine) operationByVersion(key CatalogKey, uid, subject, revision string) (OperationReceipt, bool) {
	if f.operationVersions == nil {
		f.rebuildOperationIndex()
	}
	id, ok := f.operationVersions[operationVersionKey{Key: key, UID: uid, Subject: subject, Revision: revision}]
	if !ok {
		return OperationReceipt{}, false
	}
	r, ok := f.image.Operations[id]
	return r, ok
}
func (f *machine) putOperation(r OperationReceipt) {
	if f.operationVersions == nil {
		f.rebuildOperationIndex()
	}
	if f.image.Operations == nil {
		f.image.Operations = make(map[string]OperationReceipt)
	}
	f.image.Operations[r.ID] = r
	f.operationVersions[operationVersion(r)] = r.ID
}
func (f *machine) deleteOperation(id string) {
	if r, ok := f.image.Operations[id]; ok {
		delete(f.operationVersions, operationVersion(r))
		delete(f.image.Operations, id)
	}
}
func (f *machine) pendingOperationCount() int {
	return len(f.image.Operations) + len(f.image.OperationReservations)
}

// PendingOperationForVersion is an owner-only observation. It resolves an exact
// projected revision, never a newer desired version or a different incarnation.
func (s *Store) PendingOperationForVersion(key CatalogKey, uid, subject, revision string) (OperationReceipt, bool) {
	s.fsm.mu.Lock()
	defer s.fsm.mu.Unlock()
	return s.fsm.operationByVersion(key, uid, subject, revision)
}
