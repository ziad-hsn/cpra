package persistence

// CollectionUploadNextFence predicts the canonical prefix after one exact
// encrypted row. It performs no read, authorization or mutation. The returned
// fence constrains the next append; it does not certify that this row committed. A fence at
// the maximum item count describes a completed prefix and cannot admit another
// row. The original operation's smaller byte quota remains an FSM precondition.
func CollectionUploadNextFence(operationID string, prior CollectionUploadFence, item CollectionItem) (CollectionUploadFence, error) {
	if prior.validate() != nil || item.Ordinal != prior.Uploaded+1 {
		return CollectionUploadFence{}, ErrCollectionInvalid
	}
	cost, err := collectionItemCost(operationID, item)
	if err != nil || cost < 1 || cost > maxCollectionLedgerBytes-prior.EncodedBytes {
		return CollectionUploadFence{}, ErrCollectionQuota
	}
	digest, err := collectionNextDigest(prior.ProgressDigest, item)
	if err != nil {
		return CollectionUploadFence{}, err
	}
	prior.Uploaded++
	prior.EncodedBytes += cost
	prior.ProgressDigest = digest
	return prior, nil
}
