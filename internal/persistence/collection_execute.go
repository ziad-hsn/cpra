package persistence

import "time"

// CollectionExecuteCommand is an internal, isolated transaction over an
// already-admitted original collection. It carries no plaintext, rebase guards,
// executable job or client-supplied child handle. It is not a public HTTP body.
// Each command occupies its own Raft envelope so its bounded original-plan
// verification can run before entering the atomic mutation critical section.
type CollectionExecuteCommand struct {
	SourceRetirement   *CollectionExecutionSourceRetirementFence `json:"source_retirement,omitempty"`
	Retirement         *CollectionExecutionRetirementFence       `json:"retirement,omitempty"`
	Publication        *CollectionExecutionPublication           `json:"publication,omitempty"`
	Finalize           *CollectionExecutionFinalizeFence         `json:"finalize,omitempty"`
	Action             string                                    `json:"action"`
	Binding            CollectionExecutionBinding                `json:"binding"`
	Authority          OperatorAuthority                         `json:"authority"`
	CapabilitiesDigest string                                    `json:"capabilities_digest"`
	Ordinal            uint64                                    `json:"ordinal,omitempty"`
	PreparedID         string                                    `json:"prepared_id,omitempty"`
	Prepared           *CollectionPreparedItem                   `json:"prepared,omitempty"`
}

func (c CollectionExecuteCommand) validate(at time.Time) error {
	if at.IsZero() || c.Binding.validate() != nil {
		return ErrCollectionInvalid
	}
	if c.Action == "retire_sources" {
		if c.SourceRetirement == nil || c.Retirement != nil || c.Publication != nil || c.Finalize != nil || c.Authority != (OperatorAuthority{}) || c.CapabilitiesDigest != "" || c.Ordinal != 0 || c.PreparedID != "" || c.Prepared != nil {
			return ErrCollectionInvalid
		}
		return c.SourceRetirement.validate(c.Binding, at)
	}
	if c.SourceRetirement != nil {
		return ErrCollectionInvalid
	}
	if c.Action == "retire" {
		if c.Retirement == nil || c.Publication != nil || c.Finalize != nil || c.Authority != (OperatorAuthority{}) || c.CapabilitiesDigest != "" || c.Ordinal != 0 || c.PreparedID != "" || c.Prepared != nil {
			return ErrCollectionInvalid
		}
		return c.Retirement.validate(c.Binding, at)
	}
	if c.Retirement != nil {
		return ErrCollectionInvalid
	}
	if c.Action == "publish" {
		if c.Publication == nil || c.Publication.Published > CollectionValidationMaxItems || c.Finalize != nil || c.Authority != (OperatorAuthority{}) || c.CapabilitiesDigest != "" || c.Ordinal != 0 || c.PreparedID != "" || c.Prepared != nil {
			return ErrCollectionInvalid
		}
		return nil
	}
	if c.Publication != nil {
		return ErrCollectionInvalid
	}
	if c.Action == "finalize" {
		if c.Finalize == nil || c.Finalize.validate() != nil || c.Authority != (OperatorAuthority{}) || c.CapabilitiesDigest != "" || c.Ordinal != 0 || c.PreparedID != "" || c.Prepared != nil {
			return ErrCollectionInvalid
		}
		return nil
	}
	if c.Finalize != nil || c.Authority.validate() != nil || !bootstrapHash(c.CapabilitiesDigest) {
		return ErrCollectionInvalid
	}
	switch c.Action {
	case "begin":
		if c.Ordinal != 0 || c.PreparedID != "" || c.Prepared != nil {
			return ErrCollectionInvalid
		}
	case "prepare":
		if c.Ordinal == 0 || c.Ordinal > CollectionValidationMaxItems || c.PreparedID != "" || c.Prepared == nil ||
			c.Prepared.validate() != nil || c.Prepared.Binding != c.Binding || c.Prepared.Ordinal != c.Ordinal || c.Prepared.At.After(at) {
			return ErrCollectionInvalid
		}
	case "decide":
		if c.Ordinal == 0 || c.Ordinal > CollectionValidationMaxItems || c.Prepared != nil || c.PreparedID != "" && !validOperationEpoch(c.PreparedID) {
			return ErrCollectionInvalid
		}
	default:
		return ErrCollectionInvalid
	}
	return nil
}

func (f *machine) collectionExecutionState(c CollectionExecuteCommand, at time.Time) (CollectionState, error) {
	if err := c.validate(at); err != nil {
		return CollectionState{}, err
	}
	epoch, seq, _ := ParseOperationHandle(c.Binding.OperationID)
	if epoch != f.image.OperationEpoch {
		return CollectionState{}, ErrOperationExpired
	}
	s, exists := f.image.Collections[c.Binding.OperationID]
	if !exists {
		if seq <= f.image.OperationHighWater {
			return CollectionState{}, ErrOperationExpired
		}
		return CollectionState{}, ErrOperationNotFound
	}
	binding, err := collectionExecutionBindingFor(s)
	if err != nil || binding != c.Binding || s.Phase != "applying" || s.validate() != nil || at.Before(s.Activation.At) ||
		c.CapabilitiesDigest != s.Activation.CapabilitiesDigest {
		return CollectionState{}, ErrCollectionConflict
	}
	if s.Owner == nil || s.Owner.Epoch != c.Authority.Epoch || s.Actor != c.Authority.Actor {
		return CollectionState{}, ErrOperatorAuthorityDenied
	}
	if err := f.checkOperatorAuthority(c.Authority, at); err != nil {
		return CollectionState{}, err
	}
	return s.Clone(), nil
}
