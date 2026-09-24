package persistence

import (
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

const (
	CollectionTicketLifetime = 24 * time.Hour
	maxCollectionAdmissions  = 4096
)

// CollectionAdmissionProof is produced only after application admission has
// authenticated the encrypted creation ticket. It contains no ticket or key.
// UploadID is the ticket's original random identity, never a retry allocation.
type CollectionAdmissionProof struct {
	Epoch         string    `json:"epoch"`
	RequestDigest string    `json:"request_digest"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (p CollectionAdmissionProof) validate() error {
	if !validOperationEpoch(p.Epoch) || !bootstrapHash(p.RequestDigest) || p.ExpiresAt.IsZero() {
		return ErrCollectionInvalid
	}
	return nil
}

// CollectionAdmission retains consumed-ticket identity independently of staged
// rows and history. It is authoritative replay state, not a public receipt.
type CollectionAdmission struct {
	OperationID          string    `json:"operation_id"`
	UploadID             string    `json:"upload_id"`
	Actor                string    `json:"actor"`
	Epoch                string    `json:"epoch"`
	RequestDigest        string    `json:"request_digest"`
	NormalizationProfile string    `json:"normalization_profile,omitempty"`
	IdentityFormat       string    `json:"identity_format"`
	ContentDigest        string    `json:"content_digest"`
	ItemCount            uint64    `json:"item_count"`
	CreatedAt            time.Time `json:"created_at"`
	ExpiresAt            time.Time `json:"expires_at"`
}

func collectionAdmissionFor(s CollectionState) CollectionAdmission {
	return CollectionAdmission{OperationID: s.ID, UploadID: s.UploadID, Actor: s.Actor,
		Epoch: s.Admission.Epoch, RequestDigest: s.Admission.RequestDigest,
		NormalizationProfile: s.NormalizationProfile, IdentityFormat: s.IdentityFormat, ContentDigest: s.ContentDigest, ItemCount: s.ItemCount,
		CreatedAt: s.CreatedAt, ExpiresAt: s.Admission.ExpiresAt}
}

func (a CollectionAdmission) matches(s CollectionState) bool {
	p := s.Admission
	return p != nil && a.UploadID == s.UploadID && a.Actor == s.Actor && a.Epoch == p.Epoch &&
		a.RequestDigest == p.RequestDigest && a.IdentityFormat == s.IdentityFormat && a.NormalizationProfile == s.NormalizationProfile &&
		a.ContentDigest == s.ContentDigest && a.ItemCount == s.ItemCount && a.ExpiresAt.Equal(p.ExpiresAt)
}

func (f *machine) collectionEpoch(proposed string) Result {
	if f.image.OperationEpoch == "" {
		f.image.OperationEpoch = proposed
		f.image.Version = max(f.image.Version, CatalogFormatVersion)
	}
	return Result{Allowed: true, CollectionEpoch: f.image.OperationEpoch}
}

// admitCollection checks the original consumed ticket before allocation or any
// active-header quota. It never consults history, which can be ahead of replay.
// The bool reports a final reconciliation/rejection rather than a new admission.
func (f *machine) admitCollection(s CollectionState, at time.Time) (Result, bool) {
	p := s.Admission
	if p == nil {
		if _, consumed := f.image.CollectionAdmissions[s.UploadID]; consumed {
			return Result{Err: ErrCollectionConflict}, true
		}
		return Result{}, false // Legacy internal commands; public admission requires proof.
	}
	if p.Epoch != f.image.OperationEpoch || !at.Before(p.ExpiresAt) ||
		!f.image.CollectionAdmissionWatermark.Before(p.ExpiresAt) {
		return Result{Err: ErrOperationExpired}, true
	}
	if p.ExpiresAt.After(at.Add(CollectionTicketLifetime)) {
		return Result{Err: ErrCollectionInvalid}, true
	}
	if at.After(f.image.CollectionAdmissionWatermark) {
		f.image.CollectionAdmissionWatermark = at
		f.image.Version = max(f.image.Version, CatalogFormatVersion)
	}
	for id, entry := range f.image.CollectionAdmissions {
		if !entry.ExpiresAt.After(f.image.CollectionAdmissionWatermark) {
			delete(f.image.CollectionAdmissions, id)
		}
	}
	if consumed, ok := f.image.CollectionAdmissions[s.UploadID]; ok {
		if !consumed.matches(s) {
			return Result{Err: ErrCollectionConflict}, true
		}
		if existing, ok := f.image.Collections[consumed.OperationID]; ok {
			if !consumed.matches(existing) || !existing.CreatedAt.Equal(consumed.CreatedAt) {
				return f.collectionStorageFailure(ErrCollectionInvalid), true
			}
			return collectionResult(existing), true
		}
		return Result{Allowed: true, CollectionID: consumed.OperationID}, true
	}
	// A header without its live consumption record cannot be reinterpreted as a
	// new ticket, including collision with a legacy internal upload identity.
	for _, existing := range f.image.Collections {
		if existing.UploadID == s.UploadID {
			return Result{Err: ErrCollectionConflict}, true
		}
	}
	if len(f.image.CollectionAdmissions) >= maxCollectionAdmissions {
		return Result{Err: ErrCollectionQuota}, true
	}
	return Result{}, false
}

func validateCollectionAdmissions(i image) error {
	if len(i.CollectionAdmissions) > maxCollectionAdmissions ||
		len(i.CollectionAdmissions) > 0 && !collectionFormat(i.Version) ||
		!i.CollectionAdmissionWatermark.IsZero() && !catalogFormat(i.Version) {
		return ErrCollectionInvalid
	}
	operations := make(map[string]bool, len(i.CollectionAdmissions))
	for id, entry := range i.CollectionAdmissions {
		epoch, seq, err := ParseOperationHandle(entry.OperationID)
		if err != nil || epoch != i.OperationEpoch || entry.Epoch != epoch || seq > i.OperationHighWater ||
			id != entry.UploadID || !validOperationEpoch(id) || !catalogIdentifier(entry.Actor, 128) ||
			!bootstrapHash(entry.RequestDigest) || !bootstrapHash(entry.ContentDigest) ||
			entry.IdentityFormat != commitment.Format || !validCollectionNormalizationProfile(entry.NormalizationProfile) ||
			entry.NormalizationProfile != "" && i.Version < CollectionReselectionFormatVersion ||
			entry.ItemCount == 0 || entry.ItemCount > maxCollectionItems || entry.CreatedAt.IsZero() ||
			!entry.ExpiresAt.After(entry.CreatedAt) || entry.ExpiresAt.After(entry.CreatedAt.Add(CollectionTicketLifetime)) ||
			entry.CreatedAt.After(i.CollectionAdmissionWatermark) || !entry.ExpiresAt.After(i.CollectionAdmissionWatermark) || operations[entry.OperationID] {
			return ErrCollectionInvalid
		}
		operations[entry.OperationID] = true
		if existing, ok := i.Collections[entry.OperationID]; ok &&
			(!entry.matches(existing) || !entry.CreatedAt.Equal(existing.CreatedAt)) {
			return ErrCollectionInvalid
		}
		if _, ok := i.Operations[entry.OperationID]; ok {
			return ErrCollectionInvalid
		}
		if _, ok := i.OperationReservations[entry.OperationID]; ok {
			return ErrCollectionInvalid
		}
	}
	for _, state := range i.Collections {
		p := state.Admission
		if p == nil {
			continue
		}
		if p.validate() != nil || !p.ExpiresAt.After(state.CreatedAt) || p.ExpiresAt.After(state.CreatedAt.Add(CollectionTicketLifetime)) {
			return ErrCollectionInvalid
		}
		epoch, _, _ := ParseOperationHandle(state.ID)
		if p.Epoch != epoch {
			return ErrCollectionInvalid
		}
		if epoch == i.OperationEpoch && p.ExpiresAt.After(i.CollectionAdmissionWatermark) {
			entry, ok := i.CollectionAdmissions[state.UploadID]
			if !ok || entry.OperationID != state.ID || !entry.matches(state) {
				return ErrCollectionInvalid
			}
		}
	}
	return nil
}
