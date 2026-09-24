package management

import (
	"encoding/json"
	"math"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Candidate identities are UUIDs emitted as 36 unescaped ASCII bytes. These
// placeholders are used only in a discarded size-check copy, never as IDs.
const collectionCandidateIdentityPlaceholder = "00000000-0000-4000-8000-000000000000"

type collectionCandidateLayout struct {
	Generation   int64
	Changed      bool
	EncodedBytes int
}

// collectionCandidateEncoding checks the final resource envelope after inert
// normalization and omitted-credential preservation. It does not mint an ID,
// change its inputs, encrypt a value or refresh a condition. An unchanged row
// requires no replacement; its original generation remains authoritative.
func collectionCandidateEncoding(normalized api.Resource, original *api.Resource) (collectionCandidateLayout, error) {
	shape := collectionCandidateLayout{Generation: 1, Changed: true}
	encoded := normalized
	encoded.Status = nil
	encoded.Metadata.UID = collectionCandidateIdentityPlaceholder
	encoded.Metadata.ResourceVersion = collectionCandidateIdentityPlaceholder
	if original != nil {
		if original.Kind != normalized.Kind || original.Metadata.ID != normalized.Metadata.ID || original.Metadata.UID == "" || original.Metadata.ResourceVersion == "" || original.Metadata.Generation < 1 {
			return collectionCandidateLayout{}, ErrValidation
		}
		shape.Generation = original.Metadata.Generation
		if collectionDesiredEqual(normalized, *original) {
			shape.Changed = false
			return shape, nil
		}
		encoded.Metadata.UID = original.Metadata.UID
		if !jsonEqual(normalized.Spec, original.Spec) {
			if shape.Generation == math.MaxInt64 {
				return collectionCandidateLayout{}, ErrValidation
			}
			shape.Generation++
		}
	}
	encoded.Metadata.Generation = shape.Generation
	raw, err := json.Marshal(encoded)
	defer clear(raw)
	if err != nil || len(raw) > api.MaxResourceBytes {
		return collectionCandidateLayout{}, ErrValidation
	}
	shape.EncodedBytes = len(raw)
	return shape, nil
}
