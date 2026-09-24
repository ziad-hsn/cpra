package persistence

import (
	"reflect"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// CollectionExecutionResultFormatVersion adds immutable result finalization.
// It does not authorize public activation, publication or execution cleanup.
const CollectionExecutionResultFormatVersion = 10
const collectionExecutionResultSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-10\n"
const collectionExecutionResultVersion = 1

func collectionExecutionStorageFormat(version int) bool {
	return version == CollectionExecutionFormatVersion || collectionExecutionResultStorageFormat(version)
}

func collectionExecutionResultStorageFormat(version int) bool {
	return version == CollectionExecutionResultFormatVersion || collectionExecutionPublicationStorageFormat(version)
}

func collectionExecutionCompleted(phase string) bool {
	return phase == "completed" || phase == "partial" || phase == "failed"
}

// CollectionExecutionFinalizeFence captures the complete progress and original
// stop observation. A prepared commitment remains an unaccepted candidate, and
// nil Progress means execution never began. It is not a refreshed execution grant.
type CollectionExecutionFinalizeFence struct {
	Phase                string                       `json:"phase"`
	TerminalAt           time.Time                    `json:"terminal_at,omitempty"`
	CancellationID       string                       `json:"cancellation_id,omitempty"`
	InvalidatedByRestore string                       `json:"invalidated_by_restore,omitempty"`
	Progress             *CollectionExecutionProgress `json:"progress,omitempty"`
}

func (p CollectionExecutionFinalizeFence) Clone() CollectionExecutionFinalizeFence {
	if p.Progress != nil {
		v := p.Progress.Clone()
		p.Progress = &v
	}
	return p
}

func (p CollectionExecutionFinalizeFence) validate() error {
	if p.Progress != nil && p.Progress.validate() != nil {
		return ErrCollectionInvalid
	}
	switch p.Phase {
	case "applying":
		if p.Progress == nil || !p.TerminalAt.IsZero() || p.CancellationID != "" || p.InvalidatedByRestore != "" {
			return ErrCollectionInvalid
		}
	case "canceled":
		if p.TerminalAt.IsZero() || !validOperationEpoch(p.CancellationID) || p.InvalidatedByRestore != "" {
			return ErrCollectionInvalid
		}
	case "invalidated":
		if p.TerminalAt.IsZero() || p.CancellationID != "" || !validOperationEpoch(p.InvalidatedByRestore) {
			return ErrCollectionInvalid
		}
	default:
		return ErrCollectionInvalid
	}
	return nil
}

// CollectionExecutionFinalizeFenceFor returns the original fence after
// finalization too, so an exact retry never needs a new identity or timestamp.
func CollectionExecutionFinalizeFenceFor(s CollectionState) *CollectionExecutionFinalizeFence {
	if s.Activation == nil {
		return nil
	}
	if s.ExecutionResult != nil {
		v := s.ExecutionResult.Summary.Fence.Clone()
		return &v
	}
	p := CollectionExecutionFinalizeFence{Phase: s.Phase, TerminalAt: s.TerminalAt, InvalidatedByRestore: s.InvalidatedByRestore}
	if s.Cancellation != nil {
		p.CancellationID = s.Cancellation.ID
	}
	if s.Execution != nil {
		v := s.Execution.Clone()
		p.Progress = &v
	}
	return &p
}

func collectionExecutionFinalizeFencesEqual(a, b *CollectionExecutionFinalizeFence) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	normalize := func(p CollectionExecutionFinalizeFence) CollectionExecutionFinalizeFence {
		p = p.Clone()
		p.TerminalAt = p.TerminalAt.UTC()
		if p.Progress != nil {
			p.Progress.StartedAt = p.Progress.StartedAt.UTC()
			p.Progress.LastAt = p.Progress.LastAt.UTC()
			if p.Progress.Prepared != nil {
				p.Progress.Prepared.At = p.Progress.Prepared.At.UTC()
			}
		}
		return p
	}
	return reflect.DeepEqual(normalize(*a), normalize(*b))
}

// CollectionExecutionSummary freezes only allowlisted committed metadata.
// Binding.ActivationID is its stable result identity. Fence.Progress.Accepted
// means committed catalog decisions; ChildApplied means applied children. These
// facts remain separate when every accepted child's projection fails.
type CollectionExecutionSummary struct {
	Version              int                              `json:"version"`
	Binding              CollectionExecutionBinding       `json:"binding"`
	Actor                string                           `json:"actor"`
	NormalizationProfile string                           `json:"normalization_profile,omitempty"`
	IdentityFormat       string                           `json:"identity_format"`
	ContentDigest        string                           `json:"content_digest"`
	ItemCount            uint64                           `json:"item_count"`
	ActivationAt         time.Time                        `json:"activation_at"`
	Fence                CollectionExecutionFinalizeFence `json:"fence"`
	Unattempted          uint64                           `json:"unattempted"`
	Outcome              string                           `json:"outcome"`
	FinalizedAt          time.Time                        `json:"finalized_at"`
}

func (s CollectionExecutionSummary) Clone() CollectionExecutionSummary {
	s.Fence = s.Fence.Clone()
	return s
}

func collectionExecutionOutcome(p CollectionExecutionFinalizeFence) (string, bool) {
	if p.Progress != nil && p.Progress.Accepted != p.Progress.ChildTerminals {
		return "", false
	}
	if p.Phase == "canceled" || p.Phase == "invalidated" {
		return p.Phase, true
	}
	x := p.Progress
	if p.Phase != "applying" || x == nil || x.Processed != x.ItemCount || x.Prepared != nil {
		return "", false
	}
	if x.Accepted+x.Unchanged == 0 {
		return "failed", true
	}
	if x.Accepted+x.Unchanged == x.ItemCount && x.Accepted == x.ChildApplied {
		return "completed", true
	}
	return "partial", true
}

func (s CollectionExecutionSummary) validate() error {
	if s.Version != collectionExecutionResultVersion || s.Binding.validate() != nil || !catalogIdentifier(s.Actor, 128) ||
		s.IdentityFormat != commitment.Format || !validCollectionNormalizationProfile(s.NormalizationProfile) || !bootstrapHash(s.ContentDigest) || s.ItemCount == 0 || s.ItemCount > CollectionValidationMaxItems ||
		s.ActivationAt.IsZero() || s.FinalizedAt.Before(s.ActivationAt) || s.Fence.validate() != nil || s.FinalizedAt.Before(s.Fence.TerminalAt) || s.Fence.Phase != "applying" && s.Fence.TerminalAt.Before(s.ActivationAt) {
		return ErrCollectionInvalid
	}
	processed := uint64(0)
	if p := s.Fence.Progress; p != nil {
		if p.Binding != s.Binding || p.ItemCount != s.ItemCount || !p.StartedAt.Equal(s.ActivationAt) || s.FinalizedAt.Before(p.LastAt) {
			return ErrCollectionInvalid
		}
		processed = p.Processed
	}
	outcome, ready := collectionExecutionOutcome(s.Fence)
	if !ready || outcome != s.Outcome || s.Unattempted != s.ItemCount-processed {
		return ErrCollectionInvalid
	}
	return nil
}

// Publication progress commits the retained result prefix independently of the
// immutable finalization summary. Execution cleanup still requires its own fence.
type CollectionExecutionResultState struct {
	Summary          CollectionExecutionSummary `json:"summary"`
	Published        uint64                     `json:"published,omitempty"`
	HistorySealed    bool                       `json:"history_sealed,omitempty"`
	PublishedBytes   uint64                     `json:"published_bytes,omitempty"`
	ProgressDigest   string                     `json:"progress_digest,omitempty"`
	HistoryExpiredAt time.Time                  `json:"history_expired_at,omitempty,omitzero"`
}

func (r CollectionExecutionResultState) Clone() CollectionExecutionResultState {
	r.Summary = r.Summary.Clone()
	return r
}

func (r CollectionExecutionResultState) validateState(s CollectionState) error {
	b, err := collectionExecutionBindingFor(s)
	v := r.Summary
	if err != nil || r.validatePublication() != nil || v.validate() != nil || v.Binding != b || v.Actor != s.Actor ||
		v.IdentityFormat != s.IdentityFormat || v.NormalizationProfile != s.NormalizationProfile || v.ContentDigest != s.ContentDigest || v.ItemCount != s.ItemCount || !v.ActivationAt.Equal(s.Activation.At) || v.Outcome != s.Phase {
		return ErrCollectionInvalid
	}
	// Reconstruct the pre-finalization fence without consulting the result itself.
	original := s
	original.ExecutionResult = nil
	if collectionExecutionCompleted(s.Phase) {
		if !s.TerminalAt.Equal(v.FinalizedAt) {
			return ErrCollectionInvalid
		}
		original.Phase, original.TerminalAt = "applying", time.Time{}
	}
	if !collectionExecutionFinalizeFencesEqual(&v.Fence, CollectionExecutionFinalizeFenceFor(original)) {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionExecutionSummariesEqual(a, b *CollectionExecutionSummary) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	x, y := *a, *b
	x.Fence, y.Fence = CollectionExecutionFinalizeFence{}, CollectionExecutionFinalizeFence{}
	x.ActivationAt, y.ActivationAt = x.ActivationAt.UTC(), y.ActivationAt.UTC()
	x.FinalizedAt, y.FinalizedAt = x.FinalizedAt.UTC(), y.FinalizedAt.UTC()
	return reflect.DeepEqual(x, y) && collectionExecutionFinalizeFencesEqual(&a.Fence, &b.Fence)
}

func collectionExecutionPublicationStorageFormat(version int) bool {
	return version == CollectionExecutionPublicationFormatVersion || version == CollectionExecutionRetirementFormatVersion || version == CollectionExecutionSourceRetirementFormatVersion || version == CollectionReselectionFormatVersion || externalStorageFormat(version)
}
