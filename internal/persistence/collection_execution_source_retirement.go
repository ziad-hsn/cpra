package persistence

import "time"

const collectionExecutionSourceRetirementVersion = 1

// CollectionExecutionSourceRetirementState commits the surviving original
// source prefixes after execution retirement is complete. Original descriptors
// remain immutable; existing Removed fields record exact physical progress.
// These prefix digests certify cleanup/recovery only and grant no execution.
type CollectionExecutionSourceRetirementState struct {
	Version          int       `json:"version"`
	InputDigest      string    `json:"input_digest"`
	PlanDigest       string    `json:"plan_digest"`
	ValidationDigest string    `json:"validation_digest"`
	StartedAt        time.Time `json:"started_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (p CollectionExecutionSourceRetirementState) validate() error {
	if p.Version != collectionExecutionSourceRetirementVersion || !bootstrapHash(p.InputDigest) || !bootstrapHash(p.PlanDigest) || !bootstrapHash(p.ValidationDigest) ||
		p.StartedAt.IsZero() || p.UpdatedAt.Before(p.StartedAt) || p.StartedAt.Year() < 1 || p.UpdatedAt.Year() > 9999 {
		return ErrCollectionInvalid
	}
	return nil
}

func (p CollectionExecutionSourceRetirementState) validateState(s CollectionState) error {
	r, plan, validation := s.ExecutionRetirement, s.Plan, s.Validation
	if p.validate() != nil ||
		r == nil || !r.executionComplete(s) || !collectionTerminal(s.Phase) || plan == nil || validation == nil ||
		p.StartedAt.Before(r.UpdatedAt) {
		return ErrCollectionInvalid
	}
	// Preserve all dependencies until their consuming namespace is empty.
	// Surviving verdicts require the complete plan/input; surviving plan rows
	// require the complete input. Counts and bytes are validated by each header.
	if plan.RemovedFragments != 0 && validation.RemovedRows != validation.Uploaded ||
		s.RemovedRows != 0 && (validation.RemovedRows != validation.Uploaded || plan.RemovedFragments != plan.UploadedFragments) {
		return ErrCollectionInvalid
	}
	if s.RemovedRows == 0 && p.InputDigest != s.ProgressDigest || s.RemovedRows == s.Uploaded && p.InputDigest != collectionInitialDigest() ||
		plan.RemovedFragments == 0 && p.PlanDigest != plan.ProgressDigest || plan.RemovedFragments == plan.UploadedFragments && p.PlanDigest != collectionPlanInitialDigest() ||
		validation.RemovedRows == 0 && p.ValidationDigest != validation.ProgressDigest || validation.RemovedRows == validation.Uploaded && p.ValidationDigest != CollectionValidationInitialDigest() {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionExecutionSourcesEqual(a, b *CollectionExecutionSourceRetirementState) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
	x.StartedAt, y.StartedAt = time.Time{}, time.Time{}
	x.UpdatedAt, y.UpdatedAt = time.Time{}, time.Time{}
	return x == y && a.StartedAt.Equal(b.StartedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

// A completed/partial/failed phase historically required a complete plan. Only
// the explicit validated source-retirement marker admits a shortened prefix.
func collectionExecutionSourcePlanTerminal(s CollectionState) bool {
	return collectionPlanTerminal(s.Phase) || s.ExecutionRetirement != nil && s.ExecutionRetirement.Sources != nil &&
		s.ExecutionRetirement.validateState(s) == nil
}
