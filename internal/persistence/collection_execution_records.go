package persistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

const (
	collectionExecutionRecordVersion = 1
	// A prepared record includes both base64 ciphertext and its reference
	// index. A valid 1 MiB resource with many long references exceeds 2 MiB in
	// this representation. Leave room for that duplication while keeping one
	// frame below the separate 4 MiB transaction/command envelope budget.
	collectionExecutionMaxFrame = 3 << 20
	// Every child acceptance pays for its eventual bounded terminal evidence.
	// The charge remains until the parent's results are safely retired, so a
	// full ledger cannot prevent recording a known controller outcome.
	collectionChildTerminalReserve = 4096
)

// CollectionExecutionBinding identifies the original admitted input and plan.
// It is metadata, not an authorization grant. Commands must separately check
// the current permanent principal and the authoritative parent's progress.
type CollectionExecutionBinding struct {
	OperationID  string `json:"operation_id"`
	UploadID     string `json:"upload_id"`
	ActivationID string `json:"activation_id"`
	PlanID       string `json:"plan_id"`
	PlanDigest   string `json:"plan_digest"`
}

func (b CollectionExecutionBinding) validate() error {
	if _, _, err := ParseOperationHandle(b.OperationID); err != nil || !validOperationEpoch(b.UploadID) ||
		!validOperationEpoch(b.ActivationID) || !validOperationEpoch(b.PlanID) || !bootstrapHash(b.PlanDigest) {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionExecutionBindingFor(s CollectionState) (CollectionExecutionBinding, error) {
	if s.Activation == nil || s.Plan == nil {
		return CollectionExecutionBinding{}, ErrCollectionInvalid
	}
	b := CollectionExecutionBinding{OperationID: s.ID, UploadID: s.UploadID, ActivationID: s.Activation.ID,
		PlanID: s.Plan.Header.PlanID, PlanDigest: s.Plan.Descriptor.Digest}
	return b, b.validate()
}

// CollectionPreparedItem retains one exact encrypted candidate. It contains no
// refreshed guards or child operation handle. A retry reuses these identities
// and ciphertext; catalog acceptance must still check the original plan.
type CollectionPreparedItem struct {
	Binding      CollectionExecutionBinding `json:"binding"`
	ID           string                     `json:"id"`
	Ordinal      uint64                     `json:"ordinal"`
	InputOrdinal uint64                     `json:"input_ordinal"`
	RowDigest    string                     `json:"row_digest"`
	At           time.Time                  `json:"at"`
	Record       CatalogRecord              `json:"record"`
}

func (p CollectionPreparedItem) Clone() CollectionPreparedItem {
	p.Record = p.Record.Clone()
	return p
}

func (p CollectionPreparedItem) validate() error {
	if p.Binding.validate() != nil || !validOperationEpoch(p.ID) || !collectionExecutionOrdinals(p.Ordinal, p.InputOrdinal) ||
		!bootstrapHash(p.RowDigest) || p.At.IsZero() || p.Record.validate() != nil || bootstrapOrder(p.Record.Key) == "" ||
		p.Record.Removed || p.Record.CommittedIndex != 0 || p.Record.DependentsVersion != 0 || p.Record.UpdatedAt.After(p.At) {
		return ErrCollectionInvalid
	}
	return nil
}

func (p CollectionPreparedItem) matchesOutcome(o CollectionItemOutcome) bool {
	if p.validate() != nil || o.validate() != nil || o.PreparedID != p.ID || o.Binding != p.Binding ||
		o.Ordinal != p.Ordinal || o.InputOrdinal != p.InputOrdinal || o.RowDigest != p.RowDigest || o.Key != p.Record.Key || o.At.Before(p.At) {
		return false
	}
	return o.Decision != "accepted" || o.UID == p.Record.UID && o.Revision == p.Record.Revision && o.Generation == p.Record.Generation
}

// CollectionItemOutcome is the immutable catalog decision for one original plan
// ordinal. Receipt is present only when this decision accepted a real catalog
// mutation. Controller completion is a separate immutable observation.
type CollectionItemOutcome struct {
	Binding          CollectionExecutionBinding `json:"binding"`
	Ordinal          uint64                     `json:"ordinal"`
	InputOrdinal     uint64                     `json:"input_ordinal"`
	RowDigest        string                     `json:"row_digest"`
	Key              CatalogKey                 `json:"key"`
	Source           string                     `json:"source"`
	SourceDocument   uint64                     `json:"source_document"`
	SourceItem       uint64                     `json:"source_item"`
	Decision         string                     `json:"decision"`
	PreparedID       string                     `json:"prepared_id,omitempty"`
	UID              string                     `json:"uid,omitempty"`
	Revision         string                     `json:"revision,omitempty"`
	Generation       uint64                     `json:"generation,omitempty"`
	OldVersion       string                     `json:"old_version,omitempty"`
	MutationSequence uint64                     `json:"mutation_sequence,omitempty"`
	Receipt          *OperationReceipt          `json:"receipt,omitempty"`
	CommittedIndex   uint64                     `json:"committed_index"`
	At               time.Time                  `json:"at"`
}

func (o CollectionItemOutcome) Clone() CollectionItemOutcome {
	if o.Receipt != nil {
		r := *o.Receipt
		o.Receipt = &r
	}
	return o
}

func collectionExecutionOrdinals(ordinal, input uint64) bool {
	return ordinal > 0 && ordinal <= maxCollectionItems && input > 0 && input <= maxCollectionItems
}

func validCollectionSourceCoordinates(source string, document, item uint64) bool {
	var key [commitment.KeyBytes]byte
	_, err := commitment.ItemMAC(key[:], commitment.Position{Ordinal: 1, ID: "Monitor/coordinate",
		Source: commitment.SourcePosition{Token: source, Document: document, Item: item}}, []byte("{}"))
	return err == nil
}

func (o CollectionItemOutcome) validate() error {
	if o.Binding.validate() != nil || !collectionExecutionOrdinals(o.Ordinal, o.InputOrdinal) || !bootstrapHash(o.RowDigest) ||
		o.Key.validate() != nil || bootstrapOrder(o.Key) == "" || !validCollectionSourceCoordinates(o.Source, o.SourceDocument, o.SourceItem) ||
		o.CommittedIndex == 0 || o.At.IsZero() || o.PreparedID != "" && !validOperationEpoch(o.PreparedID) ||
		o.OldVersion != "" && !catalogIdentifier(o.OldVersion, 256) {
		return ErrCollectionInvalid
	}
	switch o.Decision {
	case "accepted", "unchanged":
		if !catalogIdentifier(o.UID, 256) || !catalogIdentifier(o.Revision, 256) || o.Generation == 0 {
			return ErrCollectionInvalid
		}
		if o.Decision == "unchanged" {
			if o.Receipt != nil || o.PreparedID != "" || o.MutationSequence != 0 || o.OldVersion != o.Revision {
				return ErrCollectionInvalid
			}
			return nil
		}
		r := o.Receipt
		if r == nil || r.validate() != nil || r.Subject != "" || r.Removed || r.InvalidatedByRestore != "" ||
			r.State != "committed" || r.Outcome != "committed" || r.ID == o.Binding.OperationID || o.PreparedID == "" ||
			r.Key != o.Key || r.UID != o.UID || r.NewVersion != o.Revision || r.OldVersion != o.OldVersion || r.Generation != o.Generation ||
			r.CommittedIndex != o.CommittedIndex || !r.At.Equal(o.At) || !r.UpdatedAt.Equal(o.At) || o.MutationSequence == 0 {
			return ErrCollectionInvalid
		}
		parentEpoch, _, _ := ParseOperationHandle(o.Binding.OperationID)
		childEpoch, _, err := ParseOperationHandle(r.ID)
		if err != nil || parentEpoch != childEpoch {
			return ErrCollectionInvalid
		}
	case "conflict", "dependencyBlocked":
		if o.Receipt != nil || o.UID != "" || o.Revision != "" || o.Generation != 0 || o.MutationSequence != 0 {
			return ErrCollectionInvalid
		}
	default:
		return ErrCollectionInvalid
	}
	return nil
}

// CollectionChildObservation preserves the exact terminal disposition before a
// pending catalog receipt/link can be removed. No provider diagnostics or
// plaintext configuration belong in this bounded record.
type CollectionChildObservation struct {
	Binding              CollectionExecutionBinding `json:"binding"`
	Ordinal              uint64                     `json:"ordinal"`
	RowDigest            string                     `json:"row_digest"`
	ChildID              string                     `json:"child_id"`
	State                string                     `json:"state"`
	Outcome              string                     `json:"outcome"`
	UpdatedAt            time.Time                  `json:"updated_at"`
	InvalidatedByRestore string                     `json:"invalidated_by_restore,omitempty"`
}

func (o CollectionChildObservation) validate() error {
	if o.Binding.validate() != nil || o.Ordinal == 0 || o.Ordinal > maxCollectionItems || !bootstrapHash(o.RowDigest) ||
		o.ChildID == o.Binding.OperationID || o.UpdatedAt.IsZero() ||
		o.State != "completed" && o.State != "failed" && o.State != "partial" ||
		o.State == "completed" && o.Outcome != "applied" || o.State == "failed" && o.Outcome != "projection_failed" ||
		o.State == "partial" && o.Outcome != "superseded" ||
		o.InvalidatedByRestore != "" && (!validAuthenticationID(o.InvalidatedByRestore) || o.State != "partial") {
		return ErrCollectionInvalid
	}
	parentEpoch, _, _ := ParseOperationHandle(o.Binding.OperationID)
	childEpoch, _, err := ParseOperationHandle(o.ChildID)
	if err != nil || parentEpoch != childEpoch {
		return ErrCollectionInvalid
	}
	return nil
}

func (o CollectionChildObservation) matches(accepted CollectionItemOutcome) bool {
	if o.validate() != nil || accepted.validate() != nil || accepted.Receipt == nil || o.Binding != accepted.Binding ||
		o.Ordinal != accepted.Ordinal || o.RowDigest != accepted.RowDigest || o.ChildID != accepted.Receipt.ID || o.UpdatedAt.Before(accepted.At) {
		return false
	}
	return true
}

func collectionChildObservationFor(accepted CollectionItemOutcome, terminal OperationReceipt) (CollectionChildObservation, error) {
	if accepted.validate() != nil || accepted.Receipt == nil || terminal.validate() != nil {
		return CollectionChildObservation{}, ErrCollectionInvalid
	}
	a, b := terminal, *accepted.Receipt
	if !a.At.Equal(b.At) || a.UpdatedAt.Before(b.UpdatedAt) {
		return CollectionChildObservation{}, ErrCollectionInvalid
	}
	a.State, a.Outcome, a.UpdatedAt, a.InvalidatedByRestore, a.At = b.State, b.Outcome, b.UpdatedAt, b.InvalidatedByRestore, b.At
	if a != b {
		return CollectionChildObservation{}, ErrCollectionInvalid
	}
	o := CollectionChildObservation{Binding: accepted.Binding, Ordinal: accepted.Ordinal, RowDigest: accepted.RowDigest,
		ChildID: terminal.ID, State: terminal.State, Outcome: terminal.Outcome, UpdatedAt: terminal.UpdatedAt, InvalidatedByRestore: terminal.InvalidatedByRestore}
	return o, o.validate()
}

func (o CollectionChildObservation) receipt(accepted CollectionItemOutcome) (OperationReceipt, error) {
	if !o.matches(accepted) {
		return OperationReceipt{}, ErrCollectionInvalid
	}
	r := *accepted.Receipt
	r.State, r.Outcome, r.UpdatedAt, r.InvalidatedByRestore = o.State, o.Outcome, o.UpdatedAt, o.InvalidatedByRestore
	return r, r.validate()
}

// collectionExecutionRecord is a canonical logical ledger row. Exactly one
// payload is present. The storage wrapper carries no process-local certificates.
type collectionExecutionRecord struct {
	Version  int                         `json:"version"`
	Prepared *CollectionPreparedItem     `json:"prepared,omitempty"`
	Outcome  *CollectionItemOutcome      `json:"outcome,omitempty"`
	Terminal *CollectionChildObservation `json:"terminal,omitempty"`
}

func (r collectionExecutionRecord) identity() (operation, slot string, err error) {
	if r.Version != collectionExecutionRecordVersion {
		return "", "", ErrCollectionInvalid
	}
	switch {
	case r.Prepared != nil && r.Outcome == nil && r.Terminal == nil:
		return r.Prepared.Binding.OperationID, "prepared", r.Prepared.validate()
	case r.Prepared == nil && r.Outcome != nil && r.Terminal == nil:
		return r.Outcome.Binding.OperationID, fmt.Sprintf("outcome/%016x", r.Outcome.Ordinal), r.Outcome.validate()
	case r.Prepared == nil && r.Outcome == nil && r.Terminal != nil:
		return r.Terminal.Binding.OperationID, fmt.Sprintf("terminal/%016x", r.Terminal.Ordinal), r.Terminal.validate()
	default:
		return "", "", ErrCollectionInvalid
	}
}

func collectionExecutionEncoding(r collectionExecutionRecord) ([]byte, error) {
	if _, _, err := r.identity(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > collectionExecutionMaxFrame || r.Terminal != nil && len(raw) > collectionChildTerminalReserve {
		return nil, ErrCollectionInvalid
	}
	return raw, nil
}

func decodeCollectionExecutionRecord(raw []byte) (collectionExecutionRecord, error) {
	var r collectionExecutionRecord
	// The plan metadata prescan intentionally has short-string/small-array
	// limits; encrypted catalog bodies and reference arrays do not share them.
	// Bound the full frame, decode the concrete schema, and require the exact
	// canonical re-encoding (also rejecting duplicates and trailing content).
	if len(raw) == 0 || len(raw) > collectionExecutionMaxFrame {
		return r, errCollectionLedgerCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return collectionExecutionRecord{}, errCollectionLedgerCorrupt
	}
	canonical, err := collectionExecutionEncoding(r)
	if err != nil || !bytes.Equal(canonical, raw) {
		return collectionExecutionRecord{}, errCollectionLedgerCorrupt
	}
	return r, nil
}

// Charge is a quota reservation, distinct from encoded/allocated bytes. A
// terminal row consumes capacity already charged to its original acceptance.
func collectionExecutionCharge(r collectionExecutionRecord, raw []byte) int64 {
	if r.Terminal != nil {
		return 0
	}
	n := int64(len(raw))
	if r.Outcome != nil && r.Outcome.Receipt != nil {
		n += collectionChildTerminalReserve
	}
	return n
}
