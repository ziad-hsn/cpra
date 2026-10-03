package persistence

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

const collectionExecutionProgressVersion = 1

// CollectionPreparedCommitment identifies the one encrypted candidate without
// retaining its ciphertext or provider configuration in the parent header.
// Digest binds the complete canonical prepared ledger record, including its
// nonce, envelope and references. Identity fields support bounded transitions;
// they do not replace ledger/plan verification or current authorization.
type CollectionPreparedCommitment struct {
	ID           string     `json:"id"`
	Ordinal      uint64     `json:"ordinal"`
	InputOrdinal uint64     `json:"input_ordinal"`
	RowDigest    string     `json:"row_digest"`
	Key          CatalogKey `json:"key"`
	UID          string     `json:"uid"`
	Revision     string     `json:"revision"`
	Generation   uint64     `json:"generation"`
	At           time.Time  `json:"at"`
	Digest       string     `json:"digest"`
	EncodedBytes int64      `json:"encoded_bytes"`
}

// CollectionExecutionProgress is a proposed authoritative parent commitment.
// It does not introduce an execution command, public route or outer storage
// format. Callers must commit this header with its corresponding ledger records.
// Processed counts immutable conditional decisions, not controller completion.
// ChildInvalidated counts explicit restore observations separately from normal
// supersession. ActivityAt belongs to original staging and is never updated here.
type CollectionExecutionProgress struct {
	Version           int                           `json:"version"`
	Binding           CollectionExecutionBinding    `json:"binding"`
	ItemCount         uint64                        `json:"item_count"`
	StartedAt         time.Time                     `json:"started_at"`
	LastAt            time.Time                     `json:"last_at"`
	Processed         uint64                        `json:"processed"`
	Accepted          uint64                        `json:"accepted"`
	Unchanged         uint64                        `json:"unchanged"`
	Conflicts         uint64                        `json:"conflicts"`
	DependencyBlocked uint64                        `json:"dependency_blocked"`
	ChildTerminals    uint64                        `json:"child_terminals"`
	ChildApplied      uint64                        `json:"child_applied"`
	ChildFailed       uint64                        `json:"child_failed"`
	ChildSuperseded   uint64                        `json:"child_superseded"`
	ChildInvalidated  uint64                        `json:"child_invalidated"`
	Prepared          *CollectionPreparedCommitment `json:"prepared,omitempty"`
	OutcomeBytes      int64                         `json:"outcome_bytes"`
	TerminalBytes     int64                         `json:"terminal_bytes"`
	EncodedBytes      int64                         `json:"encoded_bytes"`
	ChargedBytes      int64                         `json:"charged_bytes"`
	OutcomeDigest     string                        `json:"outcome_digest"`
	TerminalRoot      string                        `json:"terminal_root"`
}

// NewCollectionExecutionProgress creates an empty commitment. A valid binding
// is not proof of activation: the future command must check its original parent.
func NewCollectionExecutionProgress(binding CollectionExecutionBinding, itemCount uint64, at time.Time) (CollectionExecutionProgress, error) {
	p := CollectionExecutionProgress{Version: collectionExecutionProgressVersion, Binding: binding, ItemCount: itemCount, StartedAt: at, LastAt: at,
		OutcomeDigest: collectionExecutionOutcomeInitialDigest(), TerminalRoot: collectionExecutionTerminalEmptyRoot()}
	if err := p.validate(); err != nil {
		return CollectionExecutionProgress{}, err
	}
	return p, nil
}

func (p CollectionExecutionProgress) Clone() CollectionExecutionProgress {
	if p.Prepared != nil {
		q := *p.Prepared
		p.Prepared = &q
	}
	return p
}

func (p CollectionExecutionProgress) validate() error {
	if p.Version != collectionExecutionProgressVersion || p.Binding.validate() != nil || p.StartedAt.IsZero() || p.LastAt.Before(p.StartedAt) || p.ItemCount == 0 || p.ItemCount > CollectionValidationMaxItems ||
		p.Processed > p.ItemCount || p.Accepted > p.Processed || p.Unchanged > p.Processed || p.Conflicts > p.Processed || p.DependencyBlocked > p.Processed ||
		p.Accepted+p.Unchanged+p.Conflicts+p.DependencyBlocked != p.Processed || p.ChildTerminals > p.Accepted ||
		p.ChildApplied > p.ChildTerminals || p.ChildFailed > p.ChildTerminals || p.ChildSuperseded > p.ChildTerminals || p.ChildInvalidated > p.ChildTerminals ||
		p.ChildApplied+p.ChildFailed+p.ChildSuperseded+p.ChildInvalidated != p.ChildTerminals ||
		!bootstrapHash(p.OutcomeDigest) || !bootstrapHash(p.TerminalRoot) {
		return ErrCollectionInvalid
	}
	var preparedBytes int64
	if q := p.Prepared; q != nil {
		if !validOperationEpoch(q.ID) || q.Ordinal != p.Processed+1 || q.Ordinal > p.ItemCount || q.InputOrdinal == 0 || q.InputOrdinal > p.ItemCount ||
			!bootstrapHash(q.RowDigest) || q.Key.validate() != nil || bootstrapOrder(q.Key) == "" || !catalogIdentifier(q.UID, 256) || !catalogIdentifier(q.Revision, 256) ||
			q.Generation == 0 || q.At.Before(p.StartedAt) || q.At.After(p.LastAt) || !bootstrapHash(q.Digest) || q.EncodedBytes <= 0 || q.EncodedBytes > collectionExecutionMaxFrame {
			return ErrCollectionInvalid
		}
		preparedBytes = q.EncodedBytes
	}
	if p.OutcomeBytes < 0 || p.OutcomeBytes > maxCollectionLedgerBytes || p.TerminalBytes < 0 || p.TerminalBytes > int64(p.ChildTerminals)*collectionChildTerminalReserve ||
		p.EncodedBytes < 0 || p.EncodedBytes > maxCollectionLedgerBytes || p.ChargedBytes < 0 || p.ChargedBytes > maxCollectionLedgerBytes ||
		p.EncodedBytes != p.OutcomeBytes+p.TerminalBytes+preparedBytes || p.ChargedBytes != p.OutcomeBytes+preparedBytes+int64(p.Accepted)*collectionChildTerminalReserve ||
		p.Processed == 0 && (p.OutcomeBytes != 0 || p.OutcomeDigest != collectionExecutionOutcomeInitialDigest()) ||
		p.Processed > 0 && (p.OutcomeBytes < int64(p.Processed) || p.OutcomeBytes > int64(p.Processed)*collectionExecutionMaxFrame) ||
		p.ChildTerminals == 0 && (p.TerminalBytes != 0 || p.TerminalRoot != collectionExecutionTerminalEmptyRoot()) ||
		p.ChildTerminals > 0 && p.TerminalBytes < int64(p.ChildTerminals) {
		return ErrCollectionInvalid
	}
	return nil
}

// validateState checks immutable binding only. It grants no runtime authority,
// does not validate the ledger, and cannot be used to refresh original guards.
func (p CollectionExecutionProgress) validateState(s CollectionState) error {
	b, err := collectionExecutionBindingFor(s)
	if err != nil || p.validate() != nil || s.Activation.validateState(s) != nil || p.Binding != b || p.ItemCount != s.ItemCount || !p.StartedAt.Equal(s.Activation.At) {
		return ErrCollectionInvalid
	}
	if p.Prepared != nil && p.Prepared.At.Before(s.Activation.At) {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionExecutionPreparedCommitment(candidate CollectionPreparedItem) (CollectionPreparedCommitment, error) {
	raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: collectionExecutionRecordVersion, Prepared: &candidate})
	if err != nil {
		return CollectionPreparedCommitment{}, err
	}
	h := sha256.New()
	_, _ = h.Write([]byte("cpra/collection/execution-prepared/v1\x00"))
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(raw)
	return CollectionPreparedCommitment{ID: candidate.ID, Ordinal: candidate.Ordinal, InputOrdinal: candidate.InputOrdinal, RowDigest: candidate.RowDigest,
		Key: candidate.Record.Key, UID: candidate.Record.UID, Revision: candidate.Record.Revision, Generation: candidate.Record.Generation, At: candidate.At,
		Digest: hex.EncodeToString(h.Sum(nil)), EncodedBytes: int64(len(raw))}, nil
}

func collectionExecutionOutcomeInitialDigest() string {
	h := sha256.Sum256([]byte("cpra/collection/execution-outcomes/v1\x00"))
	return hex.EncodeToString(h[:])
}

func collectionExecutionOutcomeNextDigest(previous string, raw []byte) string {
	prior, _ := hex.DecodeString(previous)
	h := sha256.New()
	_, _ = h.Write([]byte("cpra/collection/execution-outcome-prefix/v1\x00"))
	_, _ = h.Write(prior)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// withPrepared computes a detached next header; the caller must atomically
// install it only after the corresponding ledger write succeeds. Exact slot
// retries are unchanged; replacement or reusing a consumed slot is rejected.
func (p CollectionExecutionProgress) withPrepared(candidate CollectionPreparedItem) (CollectionExecutionProgress, error) {
	if p.validate() != nil || candidate.Binding != p.Binding || candidate.Ordinal != p.Processed+1 || candidate.Ordinal > p.ItemCount || candidate.InputOrdinal > p.ItemCount {
		return CollectionExecutionProgress{}, ErrCollectionInvalid
	}
	q, err := collectionExecutionPreparedCommitment(candidate)
	if err != nil {
		return CollectionExecutionProgress{}, err
	}
	if p.Prepared != nil {
		old := *p.Prepared
		old.At = q.At
		if old != q || !p.Prepared.At.Equal(q.At) {
			return CollectionExecutionProgress{}, ErrCollectionConflict
		}
		return p.Clone(), nil
	}
	if q.At.Before(p.StartedAt) {
		return CollectionExecutionProgress{}, ErrCollectionInvalid
	}
	next := p.Clone()
	if q.At.After(next.LastAt) {
		next.LastAt = q.At
	}
	next.Prepared = &q
	next.EncodedBytes += q.EncodedBytes
	next.ChargedBytes += q.EncodedBytes
	if err := next.validate(); err != nil {
		return CollectionExecutionProgress{}, err
	}
	return next, nil
}

// withOutcome appends the next contiguous decision and consumes the exact
// prepared slot where required. Repeated decisions must be reconciled against
// their existing immutable ledger row before calling this transition.
func (p CollectionExecutionProgress) withOutcome(outcome CollectionItemOutcome) (CollectionExecutionProgress, error) {
	if p.validate() != nil || outcome.Binding != p.Binding || outcome.Ordinal != p.Processed+1 || outcome.Ordinal > p.ItemCount || outcome.InputOrdinal > p.ItemCount || outcome.At.Before(p.StartedAt) {
		return CollectionExecutionProgress{}, ErrCollectionInvalid
	}
	raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: collectionExecutionRecordVersion, Outcome: &outcome})
	if err != nil {
		return CollectionExecutionProgress{}, err
	}
	if q := p.Prepared; q != nil {
		if outcome.PreparedID != q.ID || outcome.InputOrdinal != q.InputOrdinal || outcome.RowDigest != q.RowDigest || outcome.Key != q.Key || outcome.At.Before(q.At) ||
			outcome.Decision == "accepted" && (outcome.UID != q.UID || outcome.Revision != q.Revision || outcome.Generation != q.Generation) {
			return CollectionExecutionProgress{}, ErrCollectionConflict
		}
	} else if outcome.PreparedID != "" || outcome.Decision == "accepted" {
		return CollectionExecutionProgress{}, ErrCollectionConflict
	}
	next := p.Clone()
	if next.Prepared != nil {
		next.EncodedBytes -= next.Prepared.EncodedBytes
		next.ChargedBytes -= next.Prepared.EncodedBytes
		next.Prepared = nil
	}
	if outcome.At.After(next.LastAt) {
		next.LastAt = outcome.At
	}
	next.Processed++
	switch outcome.Decision {
	case "accepted":
		next.Accepted++
	case "unchanged":
		next.Unchanged++
	case "conflict":
		next.Conflicts++
	case "dependencyBlocked":
		next.DependencyBlocked++
	}
	next.OutcomeBytes += int64(len(raw))
	next.EncodedBytes += int64(len(raw))
	next.ChargedBytes += collectionExecutionCharge(collectionExecutionRecord{Outcome: &outcome}, raw)
	next.OutcomeDigest = collectionExecutionOutcomeNextDigest(p.OutcomeDigest, raw)
	if err := next.validate(); err != nil {
		return CollectionExecutionProgress{}, err
	}
	return next, nil
}

// withTerminal requires the original committed acceptance, not a caller's
// assertion of success. The atomic caller proves that ledger linkage separately.
// A fixed-size empty-leaf proof prevents replacement and double counting against
// the authoritative root even when a disposable tree cache is stale.
func (p CollectionExecutionProgress) withTerminal(accepted CollectionItemOutcome, terminal CollectionChildObservation, proof collectionExecutionTerminalProof) (CollectionExecutionProgress, error) {
	if p.validate() != nil || !terminal.matches(accepted) || accepted.Binding != p.Binding || accepted.Ordinal > p.Processed || accepted.At.Before(p.StartedAt) || terminal.UpdatedAt.Before(p.StartedAt) {
		return CollectionExecutionProgress{}, ErrCollectionInvalid
	}
	raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: collectionExecutionRecordVersion, Terminal: &terminal})
	if err != nil {
		return CollectionExecutionProgress{}, err
	}
	root, err := collectionExecutionTerminalInsert(p.TerminalRoot, p.ItemCount, terminal.Ordinal, raw, proof)
	if err != nil {
		return CollectionExecutionProgress{}, err
	}
	next := p.Clone()
	if terminal.UpdatedAt.After(next.LastAt) {
		next.LastAt = terminal.UpdatedAt
	}
	next.ChildTerminals++
	next.TerminalBytes += int64(len(raw))
	next.EncodedBytes += int64(len(raw))
	next.TerminalRoot = root
	switch {
	case terminal.InvalidatedByRestore != "":
		next.ChildInvalidated++
	case terminal.State == "completed":
		next.ChildApplied++
	case terminal.State == "failed":
		next.ChildFailed++
	default:
		next.ChildSuperseded++
	}
	if err := next.validate(); err != nil {
		return CollectionExecutionProgress{}, err
	}
	return next, nil
}
