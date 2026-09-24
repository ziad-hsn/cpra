package persistence

import "time"

// CollectionReselectionFormatVersion adds immutable source normalization
// profiles and conditional inactive-upload admission. It does not enable HTTP
// reselection, authorize execution, or change older upload replay semantics.
const CollectionReselectionFormatVersion = 14

const collectionReselectionSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-14\n"

func validCollectionNormalizationProfile(profile string) bool {
	return profile == "" || profile == "cpra.file.base.v1"
}

// CollectionUploadFence identifies the exact committed prefix observed before
// preparing a suffix row. Authority is checked in the same state-machine step
// as the prefix comparison and append. A changed prefix, including an already
// accepted row, conflicts; the caller reconciles that original ciphertext by a
// protected read rather than resubmitting a newly encrypted replacement.
type CollectionUploadFence struct {
	Uploaded       uint64            `json:"uploaded"`
	EncodedBytes   int64             `json:"encoded_bytes"`
	ProgressDigest string            `json:"progress_digest"`
	Authority      OperatorAuthority `json:"authority"`
}

func (p CollectionUploadFence) validate() error {
	if p.Uploaded >= maxCollectionItems || p.EncodedBytes < 0 || p.EncodedBytes > maxCollectionLedgerBytes ||
		!bootstrapHash(p.ProgressDigest) || p.Authority.validate() != nil ||
		(p.Uploaded == 0) != (p.EncodedBytes == 0) || p.Uploaded == 0 && p.ProgressDigest != collectionInitialDigest() {
		return ErrCollectionInvalid
	}
	return nil
}

func (f *machine) checkCollectionUploadFence(s CollectionState, p CollectionUploadFence, at time.Time) error {
	if s.Owner == nil || s.Owner.Epoch != p.Authority.Epoch || s.Actor != p.Authority.Actor {
		return ErrOperatorAuthorityDenied
	}
	if err := f.checkOperatorAuthority(p.Authority, at); err != nil {
		return err
	}
	if s.Uploaded != p.Uploaded || s.EncodedBytes != p.EncodedBytes || s.ProgressDigest != p.ProgressDigest {
		return ErrCollectionConflict
	}
	return nil
}

// Profile-bearing retirement fences contain original retained summaries. Their
// presence requires format 14 even when the action itself predates this format.
func collectionReselectionCommand(c Command) bool {
	if v := c.Collection; v != nil {
		return v.UploadFence != nil || v.Create != nil && v.Create.NormalizationProfile != ""
	}
	if v := c.CollectionExecute; v != nil {
		return collectionRetirementHasNormalization(v.Retirement) || v.SourceRetirement != nil && collectionRetirementHasNormalization(v.SourceRetirement.Retirement)
	}
	return false
}

func collectionRetirementHasNormalization(p *CollectionExecutionRetirementFence) bool {
	return p != nil && (p.Result.Summary.NormalizationProfile != "" || p.Retirement != nil && p.Retirement.Result.Summary.NormalizationProfile != "")
}
