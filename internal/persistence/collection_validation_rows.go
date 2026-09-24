package persistence

import "context"

// Snapshots verify headers, every retained original coordinate and the complete
// result prefix. At most one operation's success-plan index is retained. A
// terminal cleanup only removes tails; its original final digest remains an
// audit identity and cannot turn the shortened prefix into executable input.
func validateCollectionValidationRows(ctx context.Context, i image, view *collectionLedgerView) error {
	if ctx == nil || view == nil {
		return ErrCollectionUnavailable
	}
	type progress struct {
		count, bytes uint64
		encoded      int64
		digest       string
	}
	actual := make(map[string]progress)
	var current string
	var expected map[uint64]CollectionValidationItem
	err := view.WalkValidation(ctx, func(id string, item CollectionValidationItem) error {
		s, exists := i.Collections[id]
		if !exists || s.Validation == nil || !collectionValidationStorageFormat(i.Version) {
			return ErrCollectionInvalid
		}
		v := s.Validation
		p := actual[id]
		if p.count >= v.Uploaded-v.RemovedRows || item.Ordinal != p.count+1 {
			return ErrCollectionInvalid
		}
		if p.count == 0 {
			p.digest = CollectionValidationInitialDigest()
		}
		input, exists, err := view.item(id, item.Ordinal)
		if err != nil || !exists || !collectionValidationInputMatches(item, input) {
			return ErrCollectionInvalid
		}
		if id != current {
			if current != "" && id < current {
				return ErrCollectionInvalid
			}
			current, expected = id, nil
			if v.Header.Valid {
				if s.Plan == nil || s.Plan.RemovedFragments != 0 {
					return ErrCollectionInvalid
				}
				expected = make(map[uint64]CollectionValidationItem)
				// WalkValidation owns view.mu. Borrow a bounded frame through
				// the private accessor; calling PlanPage here would relock it.
				for ordinal := uint64(1); ordinal <= s.Plan.UploadedFragments; ordinal++ {
					if err := ctx.Err(); err != nil {
						return err
					}
					raw, err := view.encodedPlanPart(id, ordinal)
					if err != nil {
						return err
					}
					part, err := decodeCollectionPlanLedgerRow(raw)
					if err != nil || part.OperationID != id || part.Part.Ordinal != ordinal {
						return ErrCollectionInvalid
					}
					if row := part.Part.Fragment.Row; row != nil {
						if _, exists := expected[row.InputOrdinal]; exists || len(expected) >= CollectionValidationMaxItems {
							return ErrCollectionInvalid
						}
						expected[row.InputOrdinal] = collectionValidationPlanItem(*row)
					}
				}
				if uint64(len(expected)) != s.ItemCount {
					return ErrCollectionInvalid
				}
			}
		}
		if v.Header.Valid && expected[item.Ordinal] != item {
			return ErrCollectionInvalid
		}
		digest, cost, err := CollectionValidationNextDigest(p.digest, item)
		if err != nil || cost > v.ResultBytes-p.bytes {
			return ErrCollectionInvalid
		}
		encoded, err := collectionValidationLedgerCost(id, item)
		if err != nil || encoded > v.EncodedBytes-v.RemovedBytes-p.encoded {
			return ErrCollectionInvalid
		}
		p.count++
		p.bytes += cost
		p.encoded += encoded
		p.digest = digest
		actual[id] = p
		return nil
	})
	if err != nil {
		return err
	}
	for id, s := range i.Collections {
		if err := ctx.Err(); err != nil {
			return err
		}
		v := s.Validation
		if v == nil {
			continue
		}
		if !collectionValidationStorageFormat(i.Version) || v.validate(s) != nil {
			return ErrCollectionInvalid
		}
		p := actual[id]
		if p.count == 0 {
			p.digest = CollectionValidationInitialDigest()
		}
		if p.count != v.Uploaded-v.RemovedRows || p.encoded != v.EncodedBytes-v.RemovedBytes ||
			v.RemovedRows == 0 && (p.bytes != v.ResultBytes || p.digest != v.ProgressDigest) {
			return ErrCollectionInvalid
		}
	}
	return nil
}
