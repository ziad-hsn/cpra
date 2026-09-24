package persistence

import "crypto/sha256"

const (
	collectionExecutionCommitmentChunk = 128
	// Includes rounded final chunks and their pointer tables for <=64 parents.
	collectionExecutionCommitmentAuditBytes = 21 << 20
)

type collectionExecutionOutcomeChunk [collectionExecutionCommitmentChunk][sha256.Size]byte

// collectionExecutionOutcomeCommitments is a disposable immutable cache of a
// VERIFIED original outcome prefix. Matching its binding/count/digest to the
// authoritative parent is mandatory before using individual hashes. Canonical
// decoding or ledger statistics alone do not establish such certification.
// append path-copies at most 4 KiB plus <=79 pointers; a failed speculative
// transaction cannot alter the previous cache. No raw record is retained.
type collectionExecutionOutcomeCommitments struct {
	Binding CollectionExecutionBinding
	Count   uint64
	Digest  string
	chunks  []*collectionExecutionOutcomeChunk
}

func newCollectionExecutionOutcomeCommitments(binding CollectionExecutionBinding) (*collectionExecutionOutcomeCommitments, error) {
	if binding.validate() != nil {
		return nil, ErrCollectionInvalid
	}
	return &collectionExecutionOutcomeCommitments{Binding: binding, Digest: collectionExecutionOutcomeInitialDigest()}, nil
}

func collectionExecutionCommitmentCost(count uint64) (int64, error) {
	if count > CollectionValidationMaxItems {
		return 0, ErrCollectionQuota
	}
	chunks := (count + collectionExecutionCommitmentChunk - 1) / collectionExecutionCommitmentChunk
	return int64(chunks) * (collectionExecutionCommitmentChunk*sha256.Size + 8), nil
}

func (c *collectionExecutionOutcomeCommitments) matches(binding CollectionExecutionBinding, count uint64, digest string) bool {
	return c != nil && c.Binding == binding && c.Count == count && c.Digest == digest && bootstrapHash(digest) && count <= CollectionValidationMaxItems && len(c.chunks) == int((count+collectionExecutionCommitmentChunk-1)/collectionExecutionCommitmentChunk)
}

func (c *collectionExecutionOutcomeCommitments) append(outcome CollectionItemOutcome) (*collectionExecutionOutcomeCommitments, error) {
	if c == nil || !c.matches(c.Binding, c.Count, c.Digest) || c.Count == CollectionValidationMaxItems || outcome.Binding != c.Binding || outcome.Ordinal != c.Count+1 {
		return nil, ErrCollectionInvalid
	}
	raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: collectionExecutionRecordVersion, Outcome: &outcome})
	if err != nil {
		return nil, err
	}
	next := &collectionExecutionOutcomeCommitments{Binding: c.Binding, Count: c.Count + 1, Digest: collectionExecutionOutcomeNextDigest(c.Digest, raw)}
	chunk := int(c.Count / collectionExecutionCommitmentChunk)
	next.chunks = make([]*collectionExecutionOutcomeChunk, chunk+1)
	copy(next.chunks, c.chunks)
	last := new(collectionExecutionOutcomeChunk)
	if chunk < len(c.chunks) {
		if c.chunks[chunk] == nil {
			return nil, ErrCollectionInvalid
		}
		*last = *c.chunks[chunk]
	}
	last[c.Count%collectionExecutionCommitmentChunk] = sha256.Sum256(raw)
	next.chunks[chunk] = last
	return next, nil
}

func (c *collectionExecutionOutcomeCommitments) matchesRecord(record collectionExecutionRecord) bool {
	if c == nil || record.Outcome == nil || record.Prepared != nil || record.Terminal != nil || record.Outcome.Binding != c.Binding || record.Outcome.Ordinal == 0 || record.Outcome.Ordinal > c.Count || !c.matches(c.Binding, c.Count, c.Digest) {
		return false
	}
	raw, err := collectionExecutionEncoding(record)
	if err != nil {
		return false
	}
	ordinal := record.Outcome.Ordinal - 1
	chunk := c.chunks[ordinal/collectionExecutionCommitmentChunk]
	return chunk != nil && chunk[ordinal%collectionExecutionCommitmentChunk] == sha256.Sum256(raw)
}
