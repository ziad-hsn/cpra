package persistence

import "time"

// CollectionValidationMaxItems is the bounded graph-validation inventory.
// Larger uploads receive a summary rejection, never a truncated item verdict.
const CollectionValidationMaxItems = 10_000

type CollectionValidationHeader struct {
	ResultID            string            `json:"result_id"`
	OperationID         string            `json:"operation_id"`
	UploadID            string            `json:"upload_id"`
	InputProgressDigest string            `json:"input_progress_digest"`
	ItemCount           uint64            `json:"item_count"`
	Authority           OperatorAuthority `json:"authority"`
	CapabilitiesDigest  string            `json:"capabilities_digest"`
	Valid               bool              `json:"valid"`
	Issue               string            `json:"issue,omitempty"`
	SummaryOnly         bool              `json:"summary_only,omitempty"`
	PlanID              string            `json:"plan_id,omitempty"`
	PlanDigest          string            `json:"plan_digest,omitempty"`
}

type CollectionValidationBegin struct {
	Header     CollectionValidationHeader     `json:"header"`
	Descriptor CollectionValidationDescriptor `json:"descriptor"`
}

// State retains the immutable intended result and the exact committed prefix.
// EncodedBytes includes ledger wrappers; ResultBytes is canonical item framing.
// FinalizedAt means the entire verdict is durable, never that it was activated.
type CollectionValidationState struct {
	Header           CollectionValidationHeader     `json:"header"`
	Descriptor       CollectionValidationDescriptor `json:"descriptor"`
	Uploaded         uint64                         `json:"uploaded"`
	EncodedBytes     int64                          `json:"encoded_bytes"`
	ResultBytes      uint64                         `json:"result_bytes"`
	ProgressDigest   string                         `json:"progress_digest"`
	RemovedRows      uint64                         `json:"removed_rows,omitempty"`
	RemovedBytes     int64                          `json:"removed_bytes,omitempty"`
	BegunAt          time.Time                      `json:"begun_at"`
	FinalizedAt      time.Time                      `json:"finalized_at,omitempty"`
	Published        uint64                         `json:"published,omitempty"`
	HistorySealed    bool                           `json:"history_sealed,omitempty"`
	HistoryExpiredAt time.Time                      `json:"history_expired_at,omitempty"`
}

type CollectionValidationCleanup struct {
	ResultID         string    `json:"result_id"`
	Uploaded         uint64    `json:"uploaded"`
	EncodedBytes     int64     `json:"encoded_bytes"`
	ResultBytes      uint64    `json:"result_bytes"`
	ProgressDigest   string    `json:"progress_digest"`
	RemovedRows      uint64    `json:"removed_rows"`
	RemovedBytes     int64     `json:"removed_bytes"`
	FinalizedAt      time.Time `json:"finalized_at"`
	Published        uint64    `json:"published"`
	HistorySealed    bool      `json:"history_sealed"`
	HistoryExpiredAt time.Time `json:"history_expired_at"`
}

func (b CollectionValidationBegin) validate() error {
	h, d := b.Header, b.Descriptor
	if _, _, err := ParseOperationHandle(h.OperationID); err != nil || !validOperationEpoch(h.ResultID) ||
		!validOperationEpoch(h.UploadID) || !bootstrapHash(h.InputProgressDigest) || h.ItemCount == 0 || h.ItemCount > maxCollectionItems ||
		h.Authority.validate() != nil || !bootstrapHash(h.CapabilitiesDigest) || !CollectionValidationIssue(h.Issue) ||
		d.Count > CollectionValidationMaxItems || d.Bytes > CollectionValidationResultMaxBytes || !bootstrapHash(d.Digest) {
		return ErrCollectionInvalid
	}
	if h.SummaryOnly {
		if h.Valid || h.Issue != "validationLimit" || h.ItemCount <= CollectionValidationMaxItems ||
			d.Count != 0 || d.Bytes != 0 || d.Digest != CollectionValidationInitialDigest() {
			return ErrCollectionInvalid
		}
	} else if h.ItemCount > CollectionValidationMaxItems || d.Count != h.ItemCount || d.Bytes < 4*d.Count {
		return ErrCollectionInvalid
	}
	if h.Valid {
		if h.Issue != "" || !validOperationEpoch(h.PlanID) || !bootstrapHash(h.PlanDigest) {
			return ErrCollectionInvalid
		}
	} else if h.Issue == "" || h.Issue == "notEvaluated" || h.PlanID != "" || h.PlanDigest != "" {
		return ErrCollectionInvalid
	}
	return nil
}

func (v CollectionValidationState) validate(s CollectionState) error {
	if (CollectionValidationBegin{Header: v.Header, Descriptor: v.Descriptor}).validate() != nil ||
		s.Owner == nil || s.Owner.Epoch != v.Header.Authority.Epoch ||
		v.Header.OperationID != s.ID || v.Header.UploadID != s.UploadID || v.Header.Authority.Actor != s.Actor ||
		v.Header.InputProgressDigest != s.ProgressDigest || v.Header.ItemCount != s.ItemCount || s.Uploaded != s.ItemCount ||
		v.BegunAt.IsZero() || v.BegunAt.Before(s.CreatedAt) || v.BegunAt.After(s.ActivityAt) ||
		v.Uploaded > v.Descriptor.Count || v.EncodedBytes < 0 || v.EncodedBytes > maxCollectionLedgerBytes ||
		v.ResultBytes > v.Descriptor.Bytes || !bootstrapHash(v.ProgressDigest) ||
		v.RemovedRows > v.Uploaded || v.RemovedBytes < 0 || v.RemovedBytes > v.EncodedBytes ||
		(v.RemovedRows == 0) != (v.RemovedBytes == 0) || (v.RemovedRows == v.Uploaded) != (v.RemovedBytes == v.EncodedBytes) {
		return ErrCollectionInvalid
	}
	if v.Uploaded == 0 {
		if v.EncodedBytes != 0 || v.ResultBytes != 0 || v.ProgressDigest != CollectionValidationInitialDigest() {
			return ErrCollectionInvalid
		}
	} else if v.EncodedBytes == 0 || v.ResultBytes == 0 {
		return ErrCollectionInvalid
	}
	if v.Uploaded == v.Descriptor.Count && (v.ResultBytes != v.Descriptor.Bytes || v.ProgressDigest != v.Descriptor.Digest) {
		return ErrCollectionInvalid
	}
	if !v.FinalizedAt.IsZero() && (v.FinalizedAt.Before(v.BegunAt) || v.FinalizedAt.After(s.ActivityAt) || v.Uploaded != v.Descriptor.Count) {
		return ErrCollectionInvalid
	}
	if v.Published > v.Descriptor.Count || v.Published != v.Descriptor.Count && v.Published%collectionLedgerBatchLimit != 0 ||
		(v.Published != 0 || v.HistorySealed) && v.FinalizedAt.IsZero() ||
		v.HistorySealed && v.Published != v.Descriptor.Count || v.RemovedRows != 0 && !v.FinalizedAt.IsZero() && !v.HistorySealed && v.HistoryExpiredAt.IsZero() {
		return ErrCollectionInvalid
	}
	if !v.HistoryExpiredAt.IsZero() && (v.FinalizedAt.IsZero() || v.HistorySealed || v.HistoryExpiredAt.Before(v.FinalizedAt.AddDate(0, 0, 30))) {
		return ErrCollectionInvalid
	}
	if v.Header.Valid {
		if s.Plan == nil || s.Plan.FinalizedAt.IsZero() || s.Plan.Header.PlanID != v.Header.PlanID || s.Plan.Descriptor.Digest != v.Header.PlanDigest {
			return ErrCollectionInvalid
		}
	} else if s.Plan != nil {
		return ErrCollectionInvalid
	}
	if s.Phase == "uploading" || s.Phase == "validating" && (v.Header.Valid || !v.FinalizedAt.IsZero()) ||
		s.Phase == "validated" && !v.Header.Valid || s.Phase == "rejected" && (v.Header.Valid || v.FinalizedAt.IsZero()) ||
		collectionLive(s.Phase) && (v.RemovedRows != 0 || v.RemovedBytes != 0) ||
		s.RemovedRows != 0 && v.RemovedRows != v.Uploaded {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionValidationAction(action string) bool {
	return action == "validation_begin" || action == "validation_append" || action == "validation_finalize" || action == "validation_publish"
}

func collectionValidationCommand(c CollectionCommand) bool {
	return collectionValidationAction(c.Action) || c.Cleanup != nil && c.Cleanup.Validation != nil
}

func (c CollectionCommand) validateValidation() error {
	if _, _, err := ParseOperationHandle(c.OperationID); err != nil || !validOperationEpoch(c.UploadID) ||
		c.Epoch != "" || c.Create != nil || c.Item != nil || c.Cleanup != nil || c.Cancel != nil ||
		c.PlanID != "" || c.PlanBegin != nil || c.PlanFragment != nil || c.PlanFinalize != nil {
		return ErrCollectionInvalid
	}
	if c.Action == "validation_begin" {
		if c.ValidationBegin == nil || c.ValidationID != "" || len(c.ValidationItems) != 0 ||
			c.ValidationPublished != 0 ||
			c.ValidationBegin.validate() != nil || c.ValidationBegin.Header.OperationID != c.OperationID || c.ValidationBegin.Header.UploadID != c.UploadID {
			return ErrCollectionInvalid
		}
		return nil
	}
	if c.ValidationBegin != nil || !validOperationEpoch(c.ValidationID) {
		return ErrCollectionInvalid
	}
	if c.Action != "validation_publish" && c.ValidationPublished != 0 {
		return ErrCollectionInvalid
	}
	switch c.Action {
	case "validation_append":
		if len(c.ValidationItems) == 0 || len(c.ValidationItems) > collectionLedgerBatchLimit {
			return ErrCollectionInvalid
		}
		for j, item := range c.ValidationItems {
			if _, err := CollectionValidationItemEncoding(item); err != nil || j > 0 && item.Ordinal != c.ValidationItems[j-1].Ordinal+1 {
				return ErrCollectionInvalid
			}
		}
	case "validation_finalize":
		if len(c.ValidationItems) != 0 {
			return ErrCollectionInvalid
		}
	case "validation_publish":
		if len(c.ValidationItems) != 0 || c.ValidationPublished > CollectionValidationMaxItems {
			return ErrCollectionInvalid
		}
	default:
		return ErrCollectionInvalid
	}
	return nil
}

func collectionValidationCleanupFor(v *CollectionValidationState) *CollectionValidationCleanup {
	if v == nil {
		return nil
	}
	return &CollectionValidationCleanup{ResultID: v.Header.ResultID, Uploaded: v.Uploaded, EncodedBytes: v.EncodedBytes,
		ResultBytes: v.ResultBytes, ProgressDigest: v.ProgressDigest, RemovedRows: v.RemovedRows, RemovedBytes: v.RemovedBytes, FinalizedAt: v.FinalizedAt,
		Published: v.Published, HistorySealed: v.HistorySealed, HistoryExpiredAt: v.HistoryExpiredAt}
}

func collectionValidationCleanupEqual(a, b *CollectionValidationCleanup) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	x, y := *a, *b
	x.FinalizedAt, y.FinalizedAt = time.Time{}, time.Time{}
	x.HistoryExpiredAt, y.HistoryExpiredAt = time.Time{}, time.Time{}
	return x == y && a.FinalizedAt.Equal(b.FinalizedAt) && a.HistoryExpiredAt.Equal(b.HistoryExpiredAt)
}

func (v CollectionValidationCleanup) validate() error {
	if !v.HistoryExpiredAt.IsZero() && (v.FinalizedAt.IsZero() || v.HistorySealed || v.HistoryExpiredAt.Before(v.FinalizedAt.AddDate(0, 0, 30))) {
		return ErrCollectionInvalid
	}
	if v.Published > v.Uploaded || v.Published != v.Uploaded && v.Published%collectionLedgerBatchLimit != 0 ||
		(v.Published != 0 || v.HistorySealed) && v.FinalizedAt.IsZero() || v.HistorySealed && v.Published != v.Uploaded {
		return ErrCollectionInvalid
	}
	if !validOperationEpoch(v.ResultID) || v.Uploaded > CollectionValidationMaxItems || v.EncodedBytes < 0 || v.EncodedBytes > maxCollectionLedgerBytes ||
		v.ResultBytes > CollectionValidationResultMaxBytes || !bootstrapHash(v.ProgressDigest) || v.RemovedRows > v.Uploaded ||
		v.RemovedBytes < 0 || v.RemovedBytes > v.EncodedBytes || (v.Uploaded == 0) != (v.EncodedBytes == 0) ||
		(v.RemovedRows == 0) != (v.RemovedBytes == 0) || (v.RemovedRows == v.Uploaded) != (v.RemovedBytes == v.EncodedBytes) {
		return ErrCollectionInvalid
	}
	return nil
}
