package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

const CollectionExecutionPublicationFormatVersion = 11
const collectionExecutionPublicationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-11\n"
const collectionExecutionResultMaxBytes = uint64(CollectionValidationMaxItems) * ((16 << 10) + 4)

// CollectionExecutionPublication identifies a previously published prefix of
// this original result. It carries no caller-supplied item or execution grant.
type CollectionExecutionPublication struct {
	Published uint64 `json:"published"`
}

type CollectionExecutionDescriptor struct {
	Count  uint64 `json:"count"`
	Bytes  uint64 `json:"bytes"`
	Digest string `json:"digest"`
}

// CollectionExecutionReceipt is available only after every original-input row
// has been materialized and the immutable item descriptor sealed in history.
type CollectionExecutionReceipt struct {
	Summary    CollectionExecutionSummary    `json:"summary"`
	Descriptor CollectionExecutionDescriptor `json:"descriptor"`
}

func (r CollectionExecutionReceipt) Clone() CollectionExecutionReceipt {
	r.Summary = r.Summary.Clone()
	return r
}

func (r CollectionExecutionReceipt) validate() error {
	if r.Summary.validate() != nil || r.Descriptor.Count != r.Summary.ItemCount ||
		r.Descriptor.Bytes < 4*r.Descriptor.Count || r.Descriptor.Bytes > collectionExecutionResultMaxBytes || !bootstrapHash(r.Descriptor.Digest) {
		return ErrHistoryUnavailable
	}
	return nil
}

func collectionExecutionReceiptsEqual(a, b CollectionExecutionReceipt) bool {
	return a.Descriptor == b.Descriptor && collectionExecutionSummariesEqual(&a.Summary, &b.Summary)
}

func collectionExecutionResultInitialDigest() string {
	v := sha256.Sum256([]byte("cpra/collection/execution-result/v1\x00"))
	return hex.EncodeToString(v[:])
}

func collectionExecutionResultNextDigest(previous string, item CollectionExecutionItem) (string, uint64, error) {
	if !bootstrapHash(previous) {
		return "", 0, ErrCollectionInvalid
	}
	raw, err := collectionExecutionItemEncoding(item)
	if err != nil {
		return "", 0, err
	}
	prior, _ := hex.DecodeString(previous)
	h := sha256.New()
	_, _ = h.Write([]byte("cpra/collection/execution-result-prefix/v1\x00"))
	_, _ = h.Write(prior)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil)), uint64(len(raw) + 4), nil
}

func (r CollectionExecutionResultState) publicationDigest() string {
	if r.Published == 0 {
		return collectionExecutionResultInitialDigest()
	}
	return r.ProgressDigest
}

func (r CollectionExecutionResultState) hasPublication() bool {
	return r.Published != 0 || r.PublishedBytes != 0 || r.ProgressDigest != "" || r.HistorySealed || !r.HistoryExpiredAt.IsZero()
}

func (r CollectionExecutionResultState) validatePublication() error {
	if r.Published > r.Summary.ItemCount || r.PublishedBytes > r.Published*(collectionExecutionItemMaxBytes+4) ||
		r.Published == 0 && (r.PublishedBytes != 0 || r.ProgressDigest != "" || r.HistorySealed) ||
		r.Published != 0 && (r.PublishedBytes < 4*r.Published || !bootstrapHash(r.ProgressDigest)) ||
		r.HistorySealed != (r.Published == r.Summary.ItemCount) ||
		!r.HistoryExpiredAt.IsZero() && r.HistoryExpiredAt.Before(r.Summary.FinalizedAt.AddDate(0, 0, 30)) {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionExecutionReceiptFor(r CollectionExecutionResultState) CollectionExecutionReceipt {
	return CollectionExecutionReceipt{Summary: r.Summary.Clone(), Descriptor: CollectionExecutionDescriptor{
		Count: r.Published, Bytes: r.PublishedBytes, Digest: r.publicationDigest()}}
}

func (f *machine) collectionExecutionPublicationState(c CollectionExecuteCommand, at time.Time) (CollectionState, error) {
	if c.Action != "publish" || c.validate(at) != nil {
		return CollectionState{}, ErrCollectionInvalid
	}
	s, ok := f.image.Collections[c.Binding.OperationID]
	if !ok {
		return CollectionState{}, ErrOperationNotFound
	}
	b, err := collectionExecutionBindingFor(s)
	if err != nil || b != c.Binding || s.validate() != nil || s.ExecutionResult == nil ||
		at.Before(s.ExecutionResult.Summary.FinalizedAt) || c.Publication.Published > s.ExecutionResult.Published {
		return CollectionState{}, ErrCollectionConflict
	}
	// A stopped old-epoch result can be retained after restore, but this path
	// never revives an execution admission or creates a child operation.
	return s.Clone(), nil
}

// The isolated transition owns the source image while the FSM mutex is released.
// The builder closes its frozen view before returning detached bounded rows.
func (f *machine) prepareCollectionExecutionPublication(c CollectionExecuteCommand, at time.Time) (*collectionExecutionItemPage, error) {
	if f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		return nil, ErrAuthenticationResetRequired
	}
	if f.bootstrapPending() {
		return nil, ErrBootstrapPending
	}
	s, err := f.collectionExecutionPublicationState(c, at)
	if err != nil {
		return nil, err
	}
	r := s.ExecutionResult
	if r.HistorySealed || !r.HistoryExpiredAt.IsZero() || c.Publication.Published < r.Published || !at.Before(r.Summary.FinalizedAt.AddDate(0, 0, 30)) {
		return nil, nil
	}
	if f.collections == nil {
		return nil, f.collectionStorageFailure(ErrCollectionUnavailable).Err
	}
	f.collectionExecutionIndex, f.collectionExecutionLedger = nil, nil
	if f.collectionPublicationLedger != f.collections || f.collectionPublicationIndex != nil && !f.collectionPublicationIndex.matches(s) {
		f.collectionPublicationIndex, f.collectionPublicationLedger = nil, nil
	}
	ledger, observed, cached := f.collections, f.image.Index, f.collectionPublicationIndex
	view, err := ledger.Freeze()
	if err != nil {
		return nil, f.collectionStorageFailure(err).Err
	}
	i := image{Index: f.image.Index, CatalogMutationSequence: f.image.CatalogMutationSequence,
		OperationEpoch: f.image.OperationEpoch, OperationHighWater: f.image.OperationHighWater, Restore: f.image.Restore}
	f.mu.Unlock()
	page, nextCache, err := buildCollectionExecutionItemPage(context.Background(), i, s, view, cached, r.Published, collectionLedgerBatchLimit)
	f.mu.Lock()
	if ledger != f.collections || observed != f.image.Index {
		return nil, f.collectionStorageFailure(ErrCollectionInvalid).Err
	}
	if err == errCollectionExecutionIndexLimit {
		return nil, ErrCollectionQuota
	}
	if err != nil {
		return nil, f.collectionStorageFailure(err).Err
	}
	f.collectionPublicationIndex, f.collectionPublicationLedger = nextCache, ledger
	return &page, nil
}

func (f *machine) publishCollectionExecution(c CollectionExecuteCommand, at time.Time, page *collectionExecutionItemPage) Result {
	s, err := f.collectionExecutionPublicationState(c, at)
	if err != nil {
		return Result{Err: err}
	}
	r := s.ExecutionResult
	if r.HistorySealed && at.Before(r.Summary.FinalizedAt.AddDate(0, 0, 30)) || !r.HistoryExpiredAt.IsZero() || c.Publication.Published < r.Published {
		return collectionResult(s)
	}
	if !at.Before(r.Summary.FinalizedAt.AddDate(0, 0, 30)) {
		if f.history == nil {
			return f.collectionStorageFailure(ErrHistoryUnavailable)
		}
		if err := f.history.Expire(at); err != nil {
			return f.collectionStorageFailure(err)
		}
		r.HistoryExpiredAt = at
	} else {
		if page == nil || len(page.Items) == 0 || len(page.Items) > collectionLedgerBatchLimit {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
		events := make([]Event, 0, len(page.Items)+1)
		bytes := 0
		for _, item := range page.Items {
			if item.InputOrdinal != r.Published+1 || item.Binding != c.Binding {
				return f.collectionStorageFailure(ErrCollectionInvalid)
			}
			event := collectionExecutionPublicationEvent(CollectionExecutionHistory{OperationID: s.ID, ResultID: c.Binding.ActivationID, FinalizedAt: r.Summary.FinalizedAt, Item: &item})
			raw, err := json.Marshal(event)
			if err != nil || len(raw) > maxCollectionExecutionPublicationEventBytes {
				return f.collectionStorageFailure(ErrCollectionInvalid)
			}
			// Reserve bounded space for the final seal and assigned event IDs.
			if len(raw)+64 > collectionLedgerBatchBytes-(64<<10)-bytes {
				break
			}
			digest, size, err := collectionExecutionResultNextDigest(r.publicationDigest(), item)
			if err != nil || size > collectionExecutionResultMaxBytes-r.PublishedBytes {
				return f.collectionStorageFailure(ErrCollectionInvalid)
			}
			r.Published++
			r.PublishedBytes += size
			r.ProgressDigest = digest
			events = append(events, event)
			bytes += len(raw) + 64
		}
		if len(events) == 0 {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
		if r.Published == r.Summary.ItemCount {
			receipt := collectionExecutionReceiptFor(*r)
			if receipt.validate() != nil {
				return f.collectionStorageFailure(ErrCollectionInvalid)
			}
			events = append(events, collectionExecutionPublicationEvent(CollectionExecutionHistory{OperationID: s.ID, ResultID: c.Binding.ActivationID, FinalizedAt: r.Summary.FinalizedAt, Seal: &receipt}))
			r.HistorySealed = true
		}
		if s.validate() != nil {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
		f.image.Collections[s.ID] = s
		f.image.Version = max(f.image.Version, CollectionExecutionPublicationFormatVersion)
		result := collectionResult(s)
		result.Events = events
		return result
	}
	if s.validate() != nil {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	f.image.Collections[s.ID] = s
	f.image.Version = max(f.image.Version, CollectionExecutionPublicationFormatVersion)
	return collectionResult(s)
}

func (s *Store) maintainCollectionExecutionHistory(at time.Time) (bool, error) {
	// Retention can advance during offline/startup maintenance. Carry that
	// observation in the replicated command; Apply never branches on a local
	// cutoff when reconstructing historical publication after restart.
	h := s.fsm.history
	h.mu.RLock()
	cutoff := h.catalog.Cutoff
	h.mu.RUnlock()

	s.fsm.mu.RLock()
	f := s.fsm
	if f.err != nil || f.bootstrapPending() || f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		s.fsm.mu.RUnlock()
		return false, nil
	}
	type candidate struct {
		head CollectionState
		at   time.Time
	}
	var candidates []candidate
	for _, head := range f.image.Collections {
		r := head.ExecutionResult
		observed := at
		// Only an expired cohort needs the retention floor. Do not manufacture
		// a future observation for still-retained publication after a clock jump.
		if r != nil && !cutoff.IsZero() && !r.Summary.FinalizedAt.After(cutoff) && cutoff.AddDate(0, 0, 30).After(observed) {
			observed = cutoff.AddDate(0, 0, 30)
		}
		if r != nil && (!r.HistorySealed || !observed.Before(r.Summary.FinalizedAt.AddDate(0, 0, 30))) && r.HistoryExpiredAt.IsZero() && !observed.Before(r.Summary.FinalizedAt) {
			candidates = append(candidates, candidate{head.Clone(), observed})
		}
	}
	s.fsm.mu.RUnlock()
	if len(candidates) == 0 {
		return false, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].head.ID < candidates[j].head.ID })
	for _, selected := range candidates {
		r := selected.head.ExecutionResult
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		results, err := s.Submit(ctx, []Command{{Kind: "collection_execute", At: selected.at, CollectionExecute: &CollectionExecuteCommand{
			Action: "publish", Binding: r.Summary.Binding, Publication: &CollectionExecutionPublication{Published: r.Published}}}})
		cancel()
		if err != nil {
			if IsLeadershipUnavailable(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return true, nil
			}
			select {
			case <-s.stop:
				return true, nil
			default:
				return true, err
			}
		}
		if len(results) != 1 {
			return true, ErrCollectionUnavailable
		}
		if errors.Is(results[0].Err, ErrCollectionQuota) {
			continue
		}
		if errors.Is(results[0].Err, ErrCollectionConflict) || errors.Is(results[0].Err, ErrOperationNotFound) || errors.Is(results[0].Err, ErrAuthenticationResetRequired) || errors.Is(results[0].Err, ErrBootstrapPending) {
			return true, nil
		}
		if results[0].Err != nil {
			return true, results[0].Err
		}
		if !results[0].Allowed {
			return true, ErrCollectionUnavailable
		}
		return true, nil
	}
	return false, nil
}

// Snapshot/recovery recomputes the published prefix from original committed
// inputs and decisions, using the caller's already verified bounded index.
func validateCollectionExecutionPublishedPrefix(ctx context.Context, head CollectionState, view *collectionLedgerView, x *collectionExecutionIndex, proof *collectionExecutionParentRecovery) error {
	r := head.ExecutionResult
	if r == nil || r.Published == 0 {
		return nil
	}
	digest := collectionExecutionResultInitialDigest()
	var after, size uint64
	for after < r.Published {
		page, err := collectionExecutionItemsFromVerified(ctx, head, view, x, proof, after, int(min(uint64(collectionExecutionItemPageRows), r.Published-after)), collectionExecutionItemPageBytes)
		if err != nil {
			return err
		}
		if len(page.Items) == 0 || page.Next <= after || page.Next > r.Published {
			return ErrCollectionInvalid
		}
		for _, item := range page.Items {
			next, n, err := collectionExecutionResultNextDigest(digest, item)
			if err != nil {
				return err
			}
			digest = next
			size += n
		}
		after = page.Next
	}
	if size != r.PublishedBytes || digest != r.ProgressDigest {
		return ErrCollectionInvalid
	}
	return nil
}
