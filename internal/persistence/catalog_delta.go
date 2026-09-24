package persistence

import (
	"fmt"
	"slices"
	"time"
)

// preparedCatalogMutation is private, process-local work for one Apply critical
// section. It is not a command, durable authority, or permission to rebase a
// mutation. The caller must hold f.mu continuously from preparation through
// installation, with no intervening catalog/operation mutation. In particular,
// it must not retain this value across unlocking, another Apply, or restoration.
//
// Preparation owns all submitted mutable buffers and captures terminal/new
// receipts before installation. Referenced catalog payloads remain immutable
// machine-owned records; copying every referenced payload would unnecessarily
// turn a metadata-only change into an unbounded payload copy.
type preparedCatalogMutation struct {
	key           string
	record        CatalogRecord
	oldReferences []CatalogKey
	oldExtensions catalogRecordExtensions
	touched       []string
	sequence      uint64
	supersededID  string
	result        Result
}

// pendingCatalogOperation is the read-only counterpart of operationByVersion.
// The latter can initialize a derived index, which preparation must not do. A
// missing index uses the authoritative, bounded pending-operation map; normal
// initialized operation indexes retain their constant-time lookup.
func (f *machine) pendingCatalogOperation(key CatalogKey, uid, revision string) (OperationReceipt, bool) {
	version := operationVersionKey{Key: key, UID: uid, Revision: revision}
	if f.operationVersions != nil {
		id, ok := f.operationVersions[version]
		if !ok {
			return OperationReceipt{}, false
		}
		receipt, ok := f.image.Operations[id]
		return receipt, ok
	}
	for _, receipt := range f.image.Operations {
		if operationVersion(receipt) == version {
			return receipt, true
		}
	}
	return OperationReceipt{}, false
}

// prepareCatalogMutation performs the same authoritative checks as the former
// combined applyCatalog path. decodeEnvelope/validateCommand still own command
// structural validation. This function mutates neither the image nor indexes,
// publishes no events, allocates no operation handle, and performs no I/O.
func (f *machine) prepareCatalogMutation(m CatalogMutation, index uint64, at time.Time, format int) (*preparedCatalogMutation, error) {
	return f.prepareCatalogMutationConsuming(m, index, at, format, "")
}

// prepareCatalogMutationConsuming credits exactly the existing typed reservation
// which the caller has authenticated and matched by operationDigest. The caller
// must retain it until every fallible pre-installation operation has succeeded.
// This function does not consume it or mutate any operation index.
func (f *machine) prepareCatalogMutationConsuming(m CatalogMutation, index uint64, at time.Time, format int, reservationID string) (*preparedCatalogMutation, error) {
	if m.Record.Key.Kind == "JobType" {
		return nil, fmt.Errorf("JobType requires its dedicated storage contract")
	}
	credit := 0
	if reservationID != "" {
		r, exists := f.image.OperationReservations[reservationID]
		if !exists || r.ID != reservationID || reservationID != m.OperationID || r.CommandKind != "catalog" ||
			r.State != "reserved" || r.Key != m.Record.Key || r.UID != m.Record.UID || r.NewVersion != m.Record.Revision ||
			r.OldVersion != m.ExpectedRevision || r.Generation != m.Record.Generation || r.Actor != m.Actor || r.Removed != m.Record.Removed {
			return nil, ErrOperationReservation
		}
		credit = 1
	}
	key := m.Record.Key.indexKey()
	old, exists := f.image.Catalog[key]
	previous, replacesPending := f.pendingCatalogOperation(m.Record.Key, old.UID, old.Revision)
	if m.OperationID != "" && !replacesPending && f.pendingOperationCount()-credit >= maxPendingCatalogOperations {
		return nil, ErrCatalogBusy
	}
	active := exists && !old.Removed
	if m.Create {
		if active {
			return nil, fmt.Errorf("%w: create target already active", ErrCatalogConflict)
		}
		if exists && (old.UID == m.Record.UID || old.Revision == m.Record.Revision) {
			return nil, fmt.Errorf("%w: create reuses retained identity", ErrCatalogConflict)
		}
	} else {
		if !active {
			return nil, ErrCatalogNotFound
		}
		if old.UID != m.ExpectedUID || old.Revision != m.ExpectedRevision ||
			old.DependentsVersion != m.ExpectedDependentsVersion ||
			m.Record.UID != old.UID || m.Record.Revision == old.Revision ||
			!m.Record.CreatedAt.Equal(old.CreatedAt) ||
			m.Record.Generation < old.Generation || m.Record.Generation > old.Generation+1 {
			return nil, ErrCatalogConflict
		}
	}
	if m.Record.UpdatedAt.After(at) || (active && m.Record.UpdatedAt.Before(old.UpdatedAt)) {
		return nil, fmt.Errorf("%w: mutation observation time regressed", ErrCatalogConflict)
	}
	if replacesPending && at.Before(previous.UpdatedAt) {
		return nil, ErrCatalogConflict
	}
	for _, condition := range m.Conditions {
		current, ok := f.image.Catalog[condition.Key.indexKey()]
		if !ok || current.Removed || current.UID != condition.UID || current.Revision != condition.Revision ||
			(condition.ExpectedDependentsVersion != nil && current.DependentsVersion != *condition.ExpectedDependentsVersion) {
			return nil, ErrCatalogDependency
		}
	}
	if m.Record.Removed && len(f.catalogIncoming[key]) != 0 {
		return nil, ErrCatalogReferenced
	}
	if err := validateCatalogJobTypeReferences(f.image, m.Record, false); err != nil {
		return nil, err
	}
	if format < catalogRecordMinimumFormat(m.Record) {
		return nil, ErrCatalogFormatDowngrade
	}
	sequence, err := f.nextCatalogMutationSequence(format)
	if err != nil {
		return nil, err
	}

	record := m.Record.Clone()
	record.CommittedIndex = index
	if active {
		record.DependentsVersion = old.DependentsVersion
	}
	p := &preparedCatalogMutation{key: key, record: record, oldReferences: slices.Clone(old.References), oldExtensions: cloneCatalogRecordExtensions(old), sequence: sequence}
	touched := make(map[string]struct{}, len(old.References)+len(record.References))
	for _, target := range old.References {
		touched[target.indexKey()] = struct{}{}
	}
	for _, target := range record.References {
		touched[target.indexKey()] = struct{}{}
	}
	p.touched = make([]string, 0, len(touched))
	for key := range touched {
		p.touched = append(p.touched, key)
	}
	copy := record.Clone()
	p.result = Result{Allowed: true, Catalog: &copy, CatalogMutationSequence: sequence}
	if replacesPending {
		previous.State, previous.Outcome, previous.UpdatedAt = "partial", "superseded", at
		p.supersededID = previous.ID
		p.result.Events = append(p.result.Events, receiptEvent(previous))
	}
	if m.OperationID != "" {
		receipt := OperationReceipt{ID: m.OperationID, Key: record.Key, UID: record.UID, OldVersion: m.ExpectedRevision,
			NewVersion: record.Revision, Generation: record.Generation, CommittedIndex: index, Actor: m.Actor,
			At: at, UpdatedAt: at, State: "committed", Outcome: "committed", Removed: record.Removed}
		p.result.Operation = &receipt
		p.result.Events = append(p.result.Events, receiptEvent(receipt))
	}
	return p, nil
}

// installCatalogMutation installs only a successfully prepared delta while the
// same exclusive critical section remains held. It performs no validation,
// cryptography, storage I/O, or other error-returning operation. The existing
// Apply history barrier still determines whether a result may be published.
// Go allocation failure/panic is not a recoverable transaction mechanism.
func (f *machine) installCatalogMutation(p *preparedCatalogMutation) Result {
	if f.image.Catalog == nil {
		f.image.Catalog = make(map[string]CatalogRecord)
	}
	if f.catalog == nil {
		f.rebuildCatalogIndexes()
	}
	f.installCatalogRecordExtensions(p.oldExtensions, p.record)
	f.image.Catalog[p.key] = p.record
	f.noteCatalogChange(p.record)
	f.image.Version = max(f.image.Version, CatalogFormatVersion, catalogRecordMinimumFormat(p.record))
	if p.sequence != 0 {
		f.image.CatalogMutationSequence = p.sequence
		f.image.Version = max(f.image.Version, CatalogMutationFormatVersion)
	}
	if p.record.Removed {
		f.catalog.Delete(catalogItem{key: p.key})
	} else {
		f.catalog.ReplaceOrInsert(catalogItem{key: p.key, record: p.record})
	}
	for _, target := range p.oldReferences {
		targetKey := target.indexKey()
		delete(f.catalogIncoming[targetKey], p.key)
		if len(f.catalogIncoming[targetKey]) == 0 {
			delete(f.catalogIncoming, targetKey)
		}
	}
	for _, target := range p.record.References {
		targetKey := target.indexKey()
		incoming := f.catalogIncoming[targetKey]
		if incoming == nil {
			incoming = make(map[string]struct{})
			f.catalogIncoming[targetKey] = incoming
		}
		incoming[p.key] = struct{}{}
	}
	// Even unchanged reference edges receive the accepted mutation's token.
	for _, targetKey := range p.touched {
		target := f.image.Catalog[targetKey]
		target.DependentsVersion = p.record.CommittedIndex
		if p.sequence != 0 {
			target.DependentsVersion = p.sequence
		}
		f.image.Catalog[targetKey] = target
		f.catalog.ReplaceOrInsert(catalogItem{key: targetKey, record: target})
	}
	if p.supersededID != "" {
		f.deleteOperation(p.supersededID)
	}
	if p.result.Operation != nil {
		f.putOperation(*p.result.Operation)
	}
	return p.result
}
