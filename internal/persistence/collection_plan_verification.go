package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

const collectionPlanProgressDomain = "cpra/collection/plan-prefix/v1\x00"

// The committed-prefix chain is deliberately distinct from the codec's full
// artifact SHA-256. It supports bounded append/replay without persisting a Go
// hash implementation's private binary state.
func collectionPlanInitialDigest() string {
	h := sha256.Sum256([]byte(collectionPlanProgressDomain + collectionPlanMagic))
	return hex.EncodeToString(h[:])
}

func collectionPlanNextDigest(previous string, fragment CollectionPlanFragment) (string, error) {
	if !bootstrapHash(previous) || fragment.validate() != nil {
		return "", ErrCollectionPlanInvalid
	}
	raw, err := json.Marshal(fragment)
	if err != nil || len(raw) > CollectionPlanMaxFragmentBytes {
		return "", ErrCollectionPlanLimit
	}
	return collectionPlanRawDigest(previous, raw), nil
}

func collectionPlanRawDigest(previous string, raw []byte) string {
	prior, _ := hex.DecodeString(previous)
	h := sha256.New()
	_, _ = h.Write([]byte(collectionPlanProgressDomain))
	_, _ = h.Write(prior)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// collectionPlanPrefix checks committed typed fragments with the exact codec
// state machine. EOF at an unfinished fragment boundary is permitted only for
// an inactive partial prefix; it never yields a usable full-plan descriptor.
type collectionPlanPrefix struct {
	state    collectionPlanState
	expected CollectionPlanHeader
	progress string
	encoded  int64
}

func newCollectionPlanPrefix(header CollectionPlanHeader) *collectionPlanPrefix {
	p := &collectionPlanPrefix{state: newCollectionPlanState(), expected: header, progress: collectionPlanInitialDigest()}
	p.state.observe([]byte(collectionPlanMagic))
	return p
}

func (p *collectionPlanPrefix) close() { p.state.release() }

func (p *collectionPlanPrefix) add(ctx context.Context, operationID string, part CollectionPlanLedgerFragment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if part.Ordinal != p.state.fragments+1 || part.Fragment.validate() != nil {
		return ErrCollectionPlanInvalid
	}
	if part.Ordinal == 1 && (part.Fragment.Header == nil || *part.Fragment.Header != p.expected) {
		return ErrCollectionPlanInvalid
	}
	raw, err := json.Marshal(part.Fragment)
	if err != nil || len(raw) > CollectionPlanMaxFragmentBytes || uint64(len(raw))+4 > CollectionPlanMaxBytes-p.state.bytes {
		return ErrCollectionPlanLimit
	}
	cost, err := collectionPlanLedgerCost(operationID, part)
	if err != nil || cost < 0 || cost > maxCollectionLedgerBytes-p.encoded {
		return ErrCollectionPlanLimit
	}
	if err := p.state.accept(ctx, part.Fragment); err != nil {
		return err
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
	p.state.observe(size[:])
	p.state.observe(raw)
	p.state.fragments++
	p.encoded += cost
	p.progress = collectionPlanRawDigest(p.progress, raw)
	return ctx.Err()
}

func (p *collectionPlanPrefix) matches(plan *CollectionPlanState, terminal bool) error {
	if plan == nil || plan.RemovedFragments > plan.UploadedFragments || plan.RemovedBytes > plan.EncodedBytes ||
		p.state.fragments != plan.UploadedFragments-plan.RemovedFragments || p.encoded != plan.EncodedBytes-plan.RemovedBytes {
		return ErrCollectionPlanInvalid
	}
	if plan.RemovedFragments != 0 {
		// Terminal cleanup removes a tail, preserving original audit identity.
		// Its surviving prefix must still be structurally valid, but cannot
		// equal the original full-prefix byte count or commitment.
		if !terminal || p.state.finished {
			return ErrCollectionPlanInvalid
		}
		return nil
	}
	if p.state.bytes != plan.ArtifactBytes || p.progress != plan.ProgressDigest ||
		p.state.fragments > plan.Descriptor.Fragments || p.state.bytes > plan.Descriptor.Bytes {
		return ErrCollectionPlanInvalid
	}
	if p.state.fragments == plan.Descriptor.Fragments {
		if !p.state.finished || p.state.descriptor() != plan.Descriptor {
			return ErrCollectionPlanInvalid
		}
	} else if p.state.finished || !plan.FinalizedAt.IsZero() {
		return ErrCollectionPlanInvalid
	}
	return nil
}

// validateCollectionPlanRows validates the exact committed materialization,
// including namespace/header correspondence. It retains one codec state at a
// time while the frozen ledger walks operation IDs in deterministic order.
// It performs no provider work and does not grant activation authority.
func validateCollectionPlanRows(i image, view *collectionLedgerView) error {
	if view == nil {
		return ErrCollectionUnavailable
	}
	seen := make(map[string]struct{}, len(i.Collections))
	var operation string
	var prefix *collectionPlanPrefix
	defer func() {
		if prefix != nil {
			prefix.close()
		}
	}()
	finish := func() error {
		if prefix == nil {
			return nil
		}
		state := i.Collections[operation]
		err := prefix.matches(state.Plan, collectionExecutionSourcePlanTerminal(state))
		prefix.close()
		prefix = nil
		return err
	}
	err := view.WalkPlans(context.Background(), func(id string, part CollectionPlanLedgerFragment) error {
		state, exists := i.Collections[id]
		if !exists || state.Plan == nil {
			return ErrCollectionPlanInvalid
		}
		if id != operation {
			if operation != "" && id <= operation {
				return ErrCollectionPlanInvalid
			}
			if err := finish(); err != nil {
				return err
			}
			operation, prefix = id, newCollectionPlanPrefix(state.Plan.Header)
			seen[id] = struct{}{}
		}
		if row := part.Fragment.Row; row != nil {
			item, exists, err := view.item(id, row.InputOrdinal)
			if err != nil || !exists || !collectionPlanInputMatches(*row, item) {
				return ErrCollectionPlanInvalid
			}
		}
		return prefix.add(context.Background(), id, part)
	})
	if err != nil {
		return err
	}
	if err := finish(); err != nil {
		return err
	}
	for id, state := range i.Collections {
		if state.Plan == nil {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		p := newCollectionPlanPrefix(state.Plan.Header)
		err := p.matches(state.Plan, collectionExecutionSourcePlanTerminal(state))
		p.close()
		if err != nil {
			return err
		}
	}
	return nil
}

func collectionPlanTerminal(phase string) bool {
	return phase == "canceled" || phase == "expired" || phase == "invalidated"
}

func collectionPlanInputMatches(row CollectionPlanRow, item CollectionItem) bool {
	return row.InputOrdinal == item.Ordinal && row.Key == item.Key && row.Source == item.Source &&
		row.Document == item.SourceDocument && row.Item == item.SourceItem
}

// VerifyCollectionPlan checks one complete committed artifact outside the FSM.
// The returned fence identifies only those exact bytes; it is not authorization
// or a catalog-validity certificate. The caller must authorize before this
// protected lookup and recheck authorization and a fresh observed time at
// submission. No lock or transaction survives this method.
func (s *Store) VerifyCollectionPlan(ctx context.Context, id string, at time.Time) (CollectionPlanVerification, error) {
	var original CollectionState
	if err := s.readCollectionPlan(ctx, id, at, nil, func(state CollectionState, _ *collectionLedgerView) error {
		original = state.Clone()
		return nil
	}); err != nil {
		return CollectionPlanVerification{}, err
	}
	plan := original.Plan
	if plan.UploadedFragments != plan.Descriptor.Fragments || plan.ArtifactBytes != plan.Descriptor.Bytes {
		return CollectionPlanVerification{}, ErrCollectionConflict
	}
	prefix := newCollectionPlanPrefix(plan.Header)
	defer prefix.close()
	for ordinal := uint64(1); ordinal <= plan.UploadedFragments; ordinal++ {
		var frame []byte
		if err := s.readCollectionPlan(ctx, id, at, &original, func(_ CollectionState, view *collectionLedgerView) error {
			data, err := view.encodedPlanPart(id, ordinal)
			if err != nil || len(data) == 0 {
				return ErrCollectionUnavailable
			}
			frame = append([]byte(nil), data...)
			return nil
		}); err != nil {
			return CollectionPlanVerification{}, err
		}
		row, err := decodeCollectionPlanLedgerRow(frame)
		if err != nil || row.OperationID != id {
			return CollectionPlanVerification{}, ErrCollectionUnavailable
		}
		if planned := row.Part.Fragment.Row; planned != nil {
			var input []byte
			if err := s.readCollectionPlan(ctx, id, at, &original, func(_ CollectionState, view *collectionLedgerView) error {
				indexed, found, err := collectionReadIndex(view, id, planned.Key)
				if err != nil || !found || indexed != planned.InputOrdinal {
					return ErrCollectionPlanInvalid
				}
				data, err := view.encodedItem(id, planned.InputOrdinal)
				if err != nil || len(data) == 0 {
					return ErrCollectionUnavailable
				}
				input = append([]byte(nil), data...)
				return nil
			}); err != nil {
				return CollectionPlanVerification{}, err
			}
			originalItem, err := decodeCollectionLedgerRow(input)
			if err != nil || originalItem.OperationID != id || !collectionPlanInputMatches(*planned, originalItem.Item) {
				return CollectionPlanVerification{}, ErrCollectionPlanInvalid
			}
		}
		if err := prefix.add(ctx, id, row.Part); err != nil {
			return CollectionPlanVerification{}, err
		}
	}
	if err := prefix.matches(plan, false); err != nil {
		return CollectionPlanVerification{}, err
	}
	if err := s.readCollectionPlan(ctx, id, at, &original, nil); err != nil {
		return CollectionPlanVerification{}, err
	}
	return CollectionPlanVerification{
		OperationID: id, UploadID: original.UploadID, PlanID: plan.Header.PlanID,
		Descriptor: plan.Descriptor, UploadedFragments: plan.UploadedFragments,
		ArtifactBytes: plan.ArtifactBytes, EncodedBytes: plan.EncodedBytes,
		ProgressDigest: plan.ProgressDigest, InputProgressDigest: original.ProgressDigest, InputCount: original.ItemCount,
	}, nil
}

// readCollectionPlan copies at most one bounded encoded frame while holding
// FSM -> ledger locks. Codec decoding and graph work never execute in this
// critical section. The immutable state is checked again at every read and
// after verification; a final command must compare the returned fence too.
func (s *Store) readCollectionPlan(ctx context.Context, id string, at time.Time, original *CollectionState, visit func(CollectionState, *collectionLedgerView) error) error {
	if at.IsZero() {
		return ErrCollectionInvalid
	}
	epoch, sequence, err := ParseOperationHandle(id)
	if err != nil {
		return err
	}
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return err
	}
	defer s.fsm.mu.RUnlock()
	select {
	case <-s.stop:
		return ErrCollectionUnavailable
	default:
	}
	f := s.fsm
	if f.err != nil || f.bootstrapPending() || f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		return ErrCollectionUnavailable
	}
	if epoch != f.image.OperationEpoch {
		return ErrOperationExpired
	}
	state, exists := f.image.Collections[id]
	if !exists {
		if sequence <= f.image.OperationHighWater {
			return ErrOperationExpired
		}
		return ErrOperationNotFound
	}
	if collectionPlanTerminal(state.Phase) || !at.Before(state.ExpiresAt) {
		return ErrOperationExpired
	}
	if state.Plan == nil || state.Phase != "validating" && state.Phase != "validated" {
		return ErrCollectionConflict
	}
	if state.ID != id || state.Plan.RemovedFragments != 0 || state.Plan.RemovedBytes != 0 ||
		state.RemovedRows != 0 || state.RemovedBytes != 0 || state.validate() != nil || f.collections == nil {
		return ErrCollectionUnavailable
	}
	if original != nil && (original.Plan == nil || *state.Plan != *original.Plan || state.UploadID != original.UploadID ||
		state.Actor != original.Actor || state.IdentityFormat != original.IdentityFormat || state.NormalizationProfile != original.NormalizationProfile || state.ContentDigest != original.ContentDigest ||
		state.ItemCount != original.ItemCount || state.Uploaded != original.Uploaded || state.EncodedBytes != original.EncodedBytes ||
		state.ProgressDigest != original.ProgressDigest || state.Phase != original.Phase) {
		return ErrCollectionConflict
	}
	l := f.collections
	if err := collectionReadLock(ctx, &l.mu); err != nil {
		return err
	}
	defer l.mu.RUnlock()
	if l.closed {
		return ErrCollectionUnavailable
	}
	read := func(view *collectionLedgerView) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, encoded, err := view.planStats(id)
		if err != nil || count != state.Plan.UploadedFragments || encoded != state.Plan.EncodedBytes {
			return ErrCollectionUnavailable
		}
		if visit != nil {
			if err := visit(state, view); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	if l.db == nil {
		return read(&collectionLedgerView{rows: l.rows, keys: l.keys, operationBytes: l.operationBytes, bytes: l.bytes,
			planRows: l.planRows, planOperationBytes: l.planOperationBytes, planBytes: l.planBytes})
	}
	return l.db.View(func(tx *bolt.Tx) error { return read(&collectionLedgerView{tx: tx}) })
}
