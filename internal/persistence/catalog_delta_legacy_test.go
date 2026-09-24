package persistence

import (
	"fmt"
	"time"
)

// catalogDeltaLegacyApply is the exact pre-refactor applyCatalog body. It is a
// compatibility oracle for this factoring, not a second production path. Keep
// it frozen: changing behavior intentionally requires revisiting these tests.
// Base catalog.go SHA256: 4da047d9f1b0deabe58e4f1520b242ff92fa134ce829e0b7ff31fe4100bd4875
func (f *machine) catalogDeltaLegacyApply(m CatalogMutation, index uint64, at time.Time, format int) Result {
	key := m.Record.Key.indexKey()
	old, exists := f.image.Catalog[key]
	previous, replacesPending := f.operationByVersion(m.Record.Key, old.UID, "", old.Revision)
	if m.OperationID != "" && !replacesPending && f.pendingOperationCount() >= maxPendingCatalogOperations {
		return Result{Err: ErrCatalogBusy}
	}
	active := exists && !old.Removed
	if m.Create {
		if active {
			return Result{Err: fmt.Errorf("%w: create target already active", ErrCatalogConflict)}
		}
		if exists && (old.UID == m.Record.UID || old.Revision == m.Record.Revision) {
			return Result{Err: fmt.Errorf("%w: create reuses retained identity", ErrCatalogConflict)}
		}
	} else {
		if !active {
			return Result{Err: ErrCatalogNotFound}
		}
		if old.UID != m.ExpectedUID || old.Revision != m.ExpectedRevision ||
			old.DependentsVersion != m.ExpectedDependentsVersion ||
			m.Record.UID != old.UID || m.Record.Revision == old.Revision ||
			!m.Record.CreatedAt.Equal(old.CreatedAt) ||
			m.Record.Generation < old.Generation || m.Record.Generation > old.Generation+1 {
			return Result{Err: ErrCatalogConflict}
		}
	}
	if m.Record.UpdatedAt.After(at) || (active && m.Record.UpdatedAt.Before(old.UpdatedAt)) {
		return Result{Err: fmt.Errorf("%w: mutation observation time regressed", ErrCatalogConflict)}
	}
	if replacesPending && at.Before(previous.UpdatedAt) {
		return Result{Err: ErrCatalogConflict}
	}
	for _, condition := range m.Conditions {
		current, ok := f.image.Catalog[condition.Key.indexKey()]
		if !ok || current.Removed || current.UID != condition.UID || current.Revision != condition.Revision ||
			(condition.ExpectedDependentsVersion != nil && current.DependentsVersion != *condition.ExpectedDependentsVersion) {
			return Result{Err: ErrCatalogDependency}
		}
	}
	if m.Record.Removed && len(f.catalogIncoming[key]) != 0 {
		return Result{Err: ErrCatalogReferenced}
	}
	sequence, err := f.nextCatalogMutationSequence(format)
	if err != nil {
		return Result{Err: err}
	}
	if f.image.Catalog == nil {
		f.image.Catalog = make(map[string]CatalogRecord)
	}
	if f.catalog == nil {
		f.rebuildCatalogIndexes()
	}
	record := m.Record.Clone()
	record.CommittedIndex = index
	if active {
		record.DependentsVersion = old.DependentsVersion
	}
	f.image.Catalog[key] = record
	f.noteCatalogChange(record)
	f.image.Version = max(f.image.Version, CatalogFormatVersion)
	if sequence != 0 {
		f.image.CatalogMutationSequence = sequence
		f.image.Version = max(f.image.Version, CatalogMutationFormatVersion)
	}
	if record.Removed {
		f.catalog.Delete(catalogItem{key: key})
	} else {
		f.catalog.ReplaceOrInsert(catalogItem{key: key, record: record})
	}
	// Touch every old/new referenced target even when the edge did not change:
	// editing a dependent's rule can invalidate a shared-resource preflight.
	touched := make(map[string]struct{}, len(old.References)+len(record.References))
	for _, target := range old.References {
		targetKey := target.indexKey()
		delete(f.catalogIncoming[targetKey], key)
		if len(f.catalogIncoming[targetKey]) == 0 {
			delete(f.catalogIncoming, targetKey)
		}
		touched[targetKey] = struct{}{}
	}
	for _, target := range record.References {
		targetKey := target.indexKey()
		incoming := f.catalogIncoming[targetKey]
		if incoming == nil {
			incoming = make(map[string]struct{})
			f.catalogIncoming[targetKey] = incoming
		}
		incoming[key] = struct{}{}
		touched[targetKey] = struct{}{}
	}
	for targetKey := range touched {
		target := f.image.Catalog[targetKey]
		target.DependentsVersion = index
		if sequence != 0 {
			target.DependentsVersion = sequence
		}
		f.image.Catalog[targetKey] = target
		f.catalog.ReplaceOrInsert(catalogItem{key: targetKey, record: target})
	}
	copy := record.Clone()
	result := Result{Allowed: true, Catalog: &copy, CatalogMutationSequence: sequence}
	if replacesPending {
		previous.State, previous.Outcome, previous.UpdatedAt = "partial", "superseded", at
		f.deleteOperation(previous.ID)
		result.Events = append(result.Events, receiptEvent(previous))
	}
	if m.OperationID != "" {
		if f.image.Operations == nil {
			f.image.Operations = map[string]OperationReceipt{}
		}
		receipt := OperationReceipt{ID: m.OperationID, Key: record.Key, UID: record.UID, OldVersion: m.ExpectedRevision,
			NewVersion: record.Revision, Generation: record.Generation, CommittedIndex: index, Actor: m.Actor,
			At: at, UpdatedAt: at, State: "committed", Outcome: "committed", Removed: record.Removed}
		f.putOperation(receipt)
		result.Operation = &receipt
		result.Events = append(result.Events, receiptEvent(receipt))
	}
	return result
}
