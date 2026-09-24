package persistence

import (
	"context"
	"errors"
	"time"
)

// CollectionExecutionSourceRetirementFormatVersion commits verified source-prefix
// cleanup after execution retirement and retains original results in history.
const CollectionExecutionSourceRetirementFormatVersion = 13
const collectionExecutionSourceRetirementSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-13\n"

// CollectionExecutionSourceRetirementFence identifies the completed execution
// retirement and exact source prefixes observed before one bounded deletion.
type CollectionExecutionSourceRetirementFence struct {
	Retirement *CollectionExecutionRetirementFence `json:"retirement"`
	Cleanup    CollectionCleanup                   `json:"cleanup"`
}

func CollectionExecutionSourceRetirementFenceFor(s CollectionState) *CollectionExecutionSourceRetirementFence {
	if s.ExecutionRetirement == nil || !s.ExecutionRetirement.complete(s) {
		return nil
	}
	return &CollectionExecutionSourceRetirementFence{Retirement: CollectionExecutionRetirementFenceFor(s), Cleanup: collectionCleanupFor(s)}
}

func (p CollectionExecutionSourceRetirementFence) validate(binding CollectionExecutionBinding, at time.Time) error {
	if p.Retirement == nil || p.Retirement.Retirement == nil || p.Retirement.validate(binding, at) != nil || p.Cleanup.Plan == nil || p.Cleanup.Validation == nil {
		return ErrCollectionInvalid
	}
	c := CollectionCommand{Action: "cleanup", OperationID: binding.OperationID, UploadID: binding.UploadID, Cleanup: &p.Cleanup}
	return c.validate(at)
}

// compareCollectionSourceProgress returns true only for an earlier prefix of
// this same immutable original input/plan/result. Such retries remove nothing.
func compareCollectionSourceProgress(want, current CollectionCleanup) (bool, error) {
	if want.Plan == nil || want.Validation == nil || current.Plan == nil || current.Validation == nil {
		return false, ErrCollectionConflict
	}
	w, c := want, current
	wp, wv := *w.Plan, *w.Validation
	pairs := [][2]uint64{{w.RemovedRows, c.RemovedRows}, {wp.RemovedFragments, c.Plan.RemovedFragments}, {wv.RemovedRows, c.Validation.RemovedRows}}
	bytes := [][2]int64{{w.RemovedBytes, c.RemovedBytes}, {wp.RemovedBytes, c.Plan.RemovedBytes}, {wv.RemovedBytes, c.Validation.RemovedBytes}}
	earlier := false
	for n := range pairs {
		if pairs[n][0] > pairs[n][1] || bytes[n][0] > bytes[n][1] || (pairs[n][0] == pairs[n][1]) != (bytes[n][0] == bytes[n][1]) {
			return false, ErrCollectionConflict
		}
		earlier = earlier || pairs[n][0] < pairs[n][1]
	}
	wp.RemovedFragments, wp.RemovedBytes = c.Plan.RemovedFragments, c.Plan.RemovedBytes
	wv.RemovedRows, wv.RemovedBytes = c.Validation.RemovedRows, c.Validation.RemovedBytes
	if w.Uploaded != c.Uploaded || w.EncodedBytes != c.EncodedBytes || !w.ActivityAt.Equal(c.ActivityAt) ||
		!collectionPlanCleanupEqual(&wp, c.Plan) || !collectionValidationCleanupEqual(&wv, c.Validation) ||
		!collectionValidationRequestFenceEqual(w.ValidationRequest, c.ValidationRequest) || !collectionActivationFenceEqual(w.Activation, c.Activation) {
		return false, ErrCollectionConflict
	}
	return earlier, nil
}

func (f *machine) collectionExecutionSourceState(c CollectionExecuteCommand, at time.Time) (CollectionState, bool, error) {
	if c.Action != "retire_sources" || c.validate(at) != nil {
		return CollectionState{}, false, ErrCollectionInvalid
	}
	s, found := f.image.Collections[c.Binding.OperationID]
	if !found {
		return CollectionState{}, true, nil // A final-header retry has no work.
	}
	b, err := collectionExecutionBindingFor(s)
	p := c.SourceRetirement
	if err != nil || b != c.Binding || s.validate() != nil || s.ExecutionRetirement == nil || !s.ExecutionRetirement.complete(s) ||
		!collectionExecutionResultStatesEqual(p.Retirement.Result, *s.ExecutionResult, true) ||
		at.Before(s.ExecutionRetirement.UpdatedAt) ||
		!at.Before(s.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30)) && s.ExecutionResult.HistoryExpiredAt.IsZero() {
		return CollectionState{}, false, ErrCollectionConflict
	}
	want, current := p.Retirement.Retirement.Clone(), s.ExecutionRetirement.Clone()
	ws, cs := want.Sources, current.Sources
	want.Sources, current.Sources = nil, nil
	if !collectionExecutionRetirementsEqual(&want, &current) ||
		ws != nil && (cs == nil || !ws.StartedAt.Equal(cs.StartedAt) || ws.UpdatedAt.After(cs.UpdatedAt)) {
		return CollectionState{}, false, ErrCollectionConflict
	}
	earlier, err := compareCollectionSourceProgress(p.Cleanup, collectionCleanupFor(s))
	if err != nil || earlier {
		return s.Clone(), earlier, err
	}
	if !collectionExecutionRetirementsEqual(p.Retirement.Retirement, s.ExecutionRetirement) || cs != nil && at.Before(cs.UpdatedAt) {
		return CollectionState{}, false, ErrCollectionConflict
	}
	return s.Clone(), false, nil
}

func (f *machine) prepareCollectionExecutionSourceRetirement(c CollectionExecuteCommand, at time.Time) (*collectionExecutionSourceRetirementBatch, error) {
	if f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		return nil, ErrAuthenticationResetRequired
	}
	if f.bootstrapPending() {
		return nil, ErrBootstrapPending
	}
	s, replayed, err := f.collectionExecutionSourceState(c, at)
	if err != nil || replayed {
		if err != nil || s.ID == "" {
			f.collectionSourceCertificate = nil
		}
		return nil, err
	}
	if f.collections == nil {
		return nil, f.collectionStorageFailure(ErrCollectionUnavailable).Err
	}
	f.collectionExecutionIndex, f.collectionExecutionLedger = nil, nil
	f.collectionPublicationIndex, f.collectionPublicationLedger = nil, nil
	f.discardCollectionPlanPrefix(s.ID)
	f.discardCollectionValidationPlan(s.ID)
	ledger, observed, captured, cached := f.collections, f.image.Index, f.image, f.collectionSourceCertificate
	f.collectionSourceCertificate = nil
	f.mu.Unlock()
	var batch *collectionExecutionSourceRetirementBatch
	// The transition owns the source generation. Borrow the ledger read view
	// only during verification, avoiding a full memory-map clone on warm turns.
	err = ledger.read(func(view *collectionLedgerView) error {
		var err error
		if !cached.matches(ledger, s) {
			cached = nil
			cached, err = buildCollectionExecutionSourceCertificate(context.Background(), captured, s, view, ledger)
		}
		if err == nil {
			batch, _, err = cached.planRetirement(context.Background(), captured, s, view, ledger)
		}
		return err
	})
	f.mu.Lock()
	if ledger != f.collections || observed != f.image.Index {
		return nil, f.collectionStorageFailure(ErrCollectionInvalid).Err
	}
	if err != nil {
		return nil, f.collectionStorageFailure(err).Err
	}
	f.collectionSourceCertificate = cached
	return batch, nil
}

func (f *machine) retireCollectionExecutionSources(c CollectionExecuteCommand, at time.Time, batch *collectionExecutionSourceRetirementBatch) Result {
	defer func() {
		if f.err != nil {
			f.collectionSourceCertificate = nil
		}
	}()
	s, replayed, err := f.collectionExecutionSourceState(c, at)
	if err != nil {
		f.collectionSourceCertificate = nil
		return Result{Err: err}
	}
	if replayed {
		if s.ID == "" {
			f.collectionSourceCertificate = nil
			return Result{Allowed: true}
		}
		return collectionResult(s)
	}
	if batch == nil || f.history == nil {
		return f.collectionStorageFailure(ErrCollectionUnavailable)
	}
	// Persist the cutoff before removing its last source header. Earlier batches
	// retain that header and must not invalidate list cursors on every turn.
	removesHeader := batch.Namespace == "header" || batch.Namespace == "input" && batch.Removed.Rows == s.Uploaded-s.RemovedRows
	if removesHeader {
		if err := f.history.Expire(at); err != nil {
			return f.collectionStorageFailure(err)
		}
	}
	if s.ExecutionRetirement.Sources == nil {
		s.ExecutionRetirement.Sources = &CollectionExecutionSourceRetirementState{Version: 1, StartedAt: at, UpdatedAt: at}
	}
	var removed collectionLedgerDeletion
	switch batch.Namespace {
	case "validation":
		v := s.Validation
		removed, err = f.collections.DeleteValidationPage(s.ID, v.Uploaded-v.RemovedRows, v.EncodedBytes-v.RemovedBytes)
		v.RemovedRows += removed.Rows
		v.RemovedBytes += removed.EncodedBytes
	case "plan":
		p := s.Plan
		removed, err = f.collections.DeletePlanPage(s.ID, p.UploadedFragments-p.RemovedFragments, p.EncodedBytes-p.RemovedBytes)
		p.RemovedFragments += removed.Rows
		p.RemovedBytes += removed.EncodedBytes
	case "input":
		removed, err = f.collections.DeletePage(s.ID, s.Uploaded-s.RemovedRows, s.EncodedBytes-s.RemovedBytes)
		s.RemovedRows += removed.Rows
		s.RemovedBytes += removed.EncodedBytes
	case "header":
	default:
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	if err != nil || removed != batch.Removed || removed.Rows > collectionLedgerBatchLimit || removed.EncodedBytes > collectionLedgerBatchBytes {
		return f.collectionStorageFailure(errors.Join(err, ErrCollectionInvalid))
	}
	sources := s.ExecutionRetirement.Sources
	sources.UpdatedAt = at
	sources.InputDigest, sources.PlanDigest, sources.ValidationDigest = batch.InputDigest, batch.PlanDigest, batch.ValidationDigest
	if s.validate() != nil {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	if s.RemovedRows == s.Uploaded && s.Plan.RemovedFragments == s.Plan.UploadedFragments && s.Validation.RemovedRows == s.Validation.Uploaded {
		delete(f.image.Collections, s.ID)
		f.collectionSourceCertificate = nil
	} else {
		f.image.Collections[s.ID] = s
		if f.collectionSourceCertificate != nil {
			f.collectionSourceCertificate.advance(s)
		}
	}
	f.image.Version = max(f.image.Version, CollectionExecutionSourceRetirementFormatVersion)
	return collectionResult(s)
}
