package persistence

import (
	"encoding/json"
	"time"
)

// CollectionPlanState records one immutable intended artifact and its committed
// prefix. EncodedBytes counts ledger wrappers; ArtifactBytes counts the framed
// codec stream. Neither value describes allocated disk space. No field is an
// authorization grant or prepared catalog mutation.
type CollectionPlanState struct {
	Header            CollectionPlanHeader     `json:"header"`
	Descriptor        CollectionPlanDescriptor `json:"descriptor"`
	UploadedFragments uint64                   `json:"uploaded_fragments"`
	EncodedBytes      int64                    `json:"encoded_bytes"`
	ArtifactBytes     uint64                   `json:"artifact_bytes"`
	ProgressDigest    string                   `json:"progress_digest"`
	RemovedFragments  uint64                   `json:"removed_fragments,omitempty"`
	RemovedBytes      int64                    `json:"removed_bytes,omitempty"`
	BegunAt           time.Time                `json:"begun_at"`
	FinalizedAt       time.Time                `json:"finalized_at,omitempty"`
}

type CollectionPlanBegin struct {
	Header     CollectionPlanHeader     `json:"header"`
	Descriptor CollectionPlanDescriptor `json:"descriptor"`
}

// CollectionPlanVerification is a conditional internal assertion returned by
// complete artifact verification outside Apply. It is never an HTTP-supplied
// certificate, an authentication token or permission to activate a resource.
type CollectionPlanVerification struct {
	OperationID         string                   `json:"operation_id"`
	UploadID            string                   `json:"upload_id"`
	PlanID              string                   `json:"plan_id"`
	Descriptor          CollectionPlanDescriptor `json:"descriptor"`
	UploadedFragments   uint64                   `json:"uploaded_fragments"`
	ArtifactBytes       uint64                   `json:"artifact_bytes"`
	EncodedBytes        int64                    `json:"encoded_bytes"`
	ProgressDigest      string                   `json:"progress_digest"`
	InputProgressDigest string                   `json:"input_progress_digest"`
	InputCount          uint64                   `json:"input_count"`
}

type CollectionPlanCleanup struct {
	PlanID            string    `json:"plan_id"`
	UploadedFragments uint64    `json:"uploaded_fragments"`
	EncodedBytes      int64     `json:"encoded_bytes"`
	ArtifactBytes     uint64    `json:"artifact_bytes"`
	ProgressDigest    string    `json:"progress_digest"`
	RemovedFragments  uint64    `json:"removed_fragments"`
	RemovedBytes      int64     `json:"removed_bytes"`
	FinalizedAt       time.Time `json:"finalized_at"`
}

func collectionPlanDescriptorValid(d CollectionPlanDescriptor) bool {
	return d.CodecVersion == CollectionPlanCodecVersion && d.Fragments >= 4 &&
		d.Fragments <= CollectionPlanMaxBytes/4 && d.Bytes > uint64(len(collectionPlanMagic)) &&
		d.Bytes <= CollectionPlanMaxBytes && bootstrapHash(d.Digest)
}

func collectionInactive(phase string) bool {
	return phase == "uploading" || phase == "validating" || phase == "validated" || phase == "rejected"
}

func (p CollectionPlanState) validate(s CollectionState) error {
	h, d := p.Header, p.Descriptor
	if h.validate() != nil || !collectionPlanDescriptorValid(d) || h.OperationID != s.ID || h.UploadID != s.UploadID ||
		h.Actor != s.Actor || h.IdentityFormat != s.IdentityFormat || h.ContentDigest != s.ContentDigest ||
		h.InputProgressDigest != s.ProgressDigest || h.ItemCount != s.ItemCount || s.Uploaded != s.ItemCount ||
		p.BegunAt.IsZero() || p.BegunAt.Before(s.CreatedAt) || p.BegunAt.After(s.ActivityAt) ||
		p.UploadedFragments > d.Fragments || p.EncodedBytes < 0 || p.EncodedBytes > maxCollectionLedgerBytes ||
		p.ArtifactBytes < uint64(len(collectionPlanMagic)) || p.ArtifactBytes > d.Bytes || !bootstrapHash(p.ProgressDigest) ||
		p.RemovedFragments > p.UploadedFragments || p.RemovedBytes < 0 || p.RemovedBytes > p.EncodedBytes ||
		(p.RemovedFragments == 0) != (p.RemovedBytes == 0) ||
		(p.RemovedFragments == p.UploadedFragments) != (p.RemovedBytes == p.EncodedBytes) {
		return ErrCollectionInvalid
	}
	if p.UploadedFragments == 0 {
		if p.EncodedBytes != 0 || p.ArtifactBytes != uint64(len(collectionPlanMagic)) || p.ProgressDigest != collectionPlanInitialDigest() {
			return ErrCollectionInvalid
		}
	} else if p.EncodedBytes == 0 || p.ArtifactBytes == uint64(len(collectionPlanMagic)) {
		return ErrCollectionInvalid
	}
	if !p.FinalizedAt.IsZero() && (p.FinalizedAt.Before(p.BegunAt) || p.FinalizedAt.After(s.ActivityAt) ||
		p.UploadedFragments != d.Fragments || p.ArtifactBytes != d.Bytes) {
		return ErrCollectionInvalid
	}
	if s.Phase == "uploading" || s.Phase == "validating" && !p.FinalizedAt.IsZero() || s.Phase == "validated" && p.FinalizedAt.IsZero() ||
		collectionLive(s.Phase) && (p.RemovedFragments != 0 || p.RemovedBytes != 0) ||
		s.RemovedRows != 0 && p.RemovedFragments != p.UploadedFragments {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionPlanCleanupFor(p *CollectionPlanState) *CollectionPlanCleanup {
	if p == nil {
		return nil
	}
	return &CollectionPlanCleanup{PlanID: p.Header.PlanID, UploadedFragments: p.UploadedFragments, EncodedBytes: p.EncodedBytes,
		ArtifactBytes: p.ArtifactBytes, ProgressDigest: p.ProgressDigest, RemovedFragments: p.RemovedFragments,
		RemovedBytes: p.RemovedBytes, FinalizedAt: p.FinalizedAt}
}

func collectionPlanCleanupEqual(a, b *CollectionPlanCleanup) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, right := *a, *b
	left.FinalizedAt, right.FinalizedAt = time.Time{}, time.Time{}
	return left == right && a.FinalizedAt.Equal(b.FinalizedAt)
}

func (p CollectionPlanCleanup) validate() error {
	if !validOperationEpoch(p.PlanID) || p.UploadedFragments > CollectionPlanMaxBytes/4 || p.EncodedBytes < 0 ||
		p.EncodedBytes > maxCollectionLedgerBytes || p.ArtifactBytes < uint64(len(collectionPlanMagic)) ||
		p.ArtifactBytes > CollectionPlanMaxBytes || !bootstrapHash(p.ProgressDigest) ||
		p.RemovedFragments > p.UploadedFragments || p.RemovedBytes < 0 || p.RemovedBytes > p.EncodedBytes ||
		(p.UploadedFragments == 0) != (p.EncodedBytes == 0) || (p.RemovedFragments == 0) != (p.RemovedBytes == 0) ||
		(p.RemovedFragments == p.UploadedFragments) != (p.RemovedBytes == p.EncodedBytes) {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionPlanFence(s CollectionState) CollectionPlanVerification {
	p := s.Plan
	return CollectionPlanVerification{OperationID: s.ID, UploadID: s.UploadID, PlanID: p.Header.PlanID,
		Descriptor: p.Descriptor, UploadedFragments: p.UploadedFragments, ArtifactBytes: p.ArtifactBytes, EncodedBytes: p.EncodedBytes,
		ProgressDigest: p.ProgressDigest, InputProgressDigest: s.ProgressDigest, InputCount: s.ItemCount}
}

func (c CollectionCommand) validatePlan(at time.Time) error {
	if _, _, err := ParseOperationHandle(c.OperationID); err != nil || !validOperationEpoch(c.UploadID) ||
		c.Epoch != "" || c.Create != nil || c.Item != nil || c.Cleanup != nil || c.Cancel != nil {
		return ErrCollectionInvalid
	}
	switch c.Action {
	case "plan_begin":
		p := c.PlanBegin
		if p == nil || c.PlanFragment != nil || c.PlanFinalize != nil || c.PlanID != "" || p.Header.validate() != nil ||
			!collectionPlanDescriptorValid(p.Descriptor) || p.Header.OperationID != c.OperationID || p.Header.UploadID != c.UploadID {
			return ErrCollectionInvalid
		}
	case "plan_append":
		if c.PlanBegin != nil || c.PlanFinalize != nil || !validOperationEpoch(c.PlanID) || c.PlanFragment == nil ||
			c.PlanFragment.Ordinal == 0 || c.PlanFragment.Ordinal > CollectionPlanMaxBytes/4 || c.PlanFragment.Fragment.validate() != nil {
			return ErrCollectionInvalid
		}
		// Validate bounds before proposal serialization; part validation does not
		// infer a successful whole-artifact verdict from an individual fragment.
		raw, err := json.Marshal(c.PlanFragment.Fragment)
		if err != nil || len(raw) > CollectionPlanMaxFragmentBytes {
			return ErrCollectionInvalid
		}
	case "plan_finalize":
		p := c.PlanFinalize
		if p == nil || c.PlanBegin != nil || c.PlanFragment != nil || c.PlanID != "" ||
			p.OperationID != c.OperationID || p.UploadID != c.UploadID || !validOperationEpoch(p.PlanID) ||
			!collectionPlanDescriptorValid(p.Descriptor) || p.UploadedFragments != p.Descriptor.Fragments ||
			p.ArtifactBytes != p.Descriptor.Bytes || p.EncodedBytes <= 0 || p.EncodedBytes > maxCollectionLedgerBytes ||
			!bootstrapHash(p.ProgressDigest) || !bootstrapHash(p.InputProgressDigest) || p.InputCount == 0 || p.InputCount > maxCollectionItems {
			return ErrCollectionInvalid
		}
	default:
		return ErrCollectionInvalid
	}
	return nil
}
