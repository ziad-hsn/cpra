package persistence

import (
	"slices"
	"time"
)

// CollectionExecutionRetirementFormatVersion adds committed, terminal-only
// execution-prefix retirement. Earlier envelopes retain their cleanup contract.
const CollectionExecutionRetirementFormatVersion = 12
const collectionExecutionRetirementSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-12\n"
const collectionExecutionRetirementVersion = 1

// CollectionExecutionRetirementState preserves the original final result and
// the exact retired decision prefix. Execution remains the immutable original
// progress; its charged bytes are not the remaining physical ledger charge.
// Source input, plan and validation rows remain intact in this format's first
// retirement phase. This state never grants permission to resume execution.
type CollectionExecutionRetirementState struct {
	Version         int                                       `json:"version"`
	Result          CollectionExecutionResultState            `json:"result"`
	Checkpoint      *CollectionExecutionRetirementCheckpoint  `json:"checkpoint,omitempty"`
	PreparedRemoved bool                                      `json:"prepared_removed,omitempty"`
	Sources         *CollectionExecutionSourceRetirementState `json:"sources,omitempty"`
	StartedAt       time.Time                                 `json:"started_at"`
	UpdatedAt       time.Time                                 `json:"updated_at"`
}

func (r CollectionExecutionRetirementState) Clone() CollectionExecutionRetirementState {
	r.Result = r.Result.Clone()
	if r.Checkpoint != nil {
		p := r.Checkpoint.Clone()
		r.Checkpoint = &p
	}
	if r.Sources != nil {
		p := *r.Sources
		r.Sources = &p
	}
	return r
}

func (r CollectionExecutionRetirementState) validateState(s CollectionState) error {
	if r.Version != collectionExecutionRetirementVersion || s.ExecutionResult == nil ||
		r.Result.Summary.validate() != nil || r.Result.validatePublication() != nil ||
		!collectionExecutionResultStatesEqual(r.Result, *s.ExecutionResult, true) ||
		!r.Result.HistorySealed && r.Result.HistoryExpiredAt.IsZero() ||
		r.StartedAt.Before(r.Result.Summary.FinalizedAt) || r.StartedAt.Before(r.Result.HistoryExpiredAt) ||
		r.UpdatedAt.Before(r.StartedAt) || !collectionTerminal(s.Phase) ||
		s.Plan == nil || s.Validation == nil {
		return ErrCollectionInvalid
	}
	if s.Execution == nil {
		if r.Checkpoint != nil || r.PreparedRemoved {
			return ErrCollectionInvalid
		}
	} else {
		p, original := r.Checkpoint, s.Execution
		if p == nil || p.validate() != nil || original.validate() != nil || original.Accepted != original.ChildTerminals ||
			p.Progress.Binding != original.Binding || p.Progress.ItemCount != original.ItemCount || !p.Progress.StartedAt.Equal(original.StartedAt) ||
			p.Progress.Processed > original.Processed || p.Progress.Accepted > original.Accepted || p.Progress.Unchanged > original.Unchanged ||
			p.Progress.Conflicts > original.Conflicts || p.Progress.DependencyBlocked > original.DependencyBlocked ||
			p.Progress.ChildApplied > original.ChildApplied || p.Progress.ChildFailed > original.ChildFailed ||
			p.Progress.ChildSuperseded > original.ChildSuperseded || p.Progress.ChildInvalidated > original.ChildInvalidated ||
			p.Progress.OutcomeBytes > original.OutcomeBytes || p.Progress.TerminalBytes > original.TerminalBytes ||
			p.Progress.EncodedBytes > original.EncodedBytes || p.Progress.ChargedBytes > original.ChargedBytes || p.Progress.LastAt.After(original.LastAt) ||
			r.PreparedRemoved && (original.Prepared == nil || p.Progress.Processed != original.Processed) {
			return ErrCollectionInvalid
		}
		if p.Progress.Processed == original.Processed && !p.matchesFinal(*original) {
			return ErrCollectionInvalid
		}
	}
	if r.Sources != nil {
		return r.Sources.validateState(s)
	}
	if s.RemovedRows != 0 || s.RemovedBytes != 0 || s.Plan.RemovedFragments != 0 || s.Plan.RemovedBytes != 0 || s.Validation.RemovedRows != 0 || s.Validation.RemovedBytes != 0 {
		return ErrCollectionInvalid
	}
	return nil
}

func (r CollectionExecutionRetirementState) complete(s CollectionState) bool {
	return r.validateState(s) == nil && r.executionComplete(s)
}

// Shape validation occurs before this predicate; keeping it separate avoids a
// recursive validation cycle when Sources requires completed execution removal.
func (r CollectionExecutionRetirementState) executionComplete(s CollectionState) bool {
	return s.Execution == nil && r.Checkpoint == nil && !r.PreparedRemoved || s.Execution != nil && r.Checkpoint != nil &&
		r.Checkpoint.Progress.Processed == s.Execution.Processed && (s.Execution.Prepared == nil || r.PreparedRemoved)
}

// Later history expiry may advance independently of an already sealed result.
// It cannot alter any original publication or execution commitment.
func collectionExecutionResultStatesEqual(a, b CollectionExecutionResultState, allowLaterExpiry bool) bool {
	if !collectionExecutionSummariesEqual(&a.Summary, &b.Summary) || a.Published != b.Published ||
		a.PublishedBytes != b.PublishedBytes || a.ProgressDigest != b.ProgressDigest || a.HistorySealed != b.HistorySealed {
		return false
	}
	return a.HistoryExpiredAt.Equal(b.HistoryExpiredAt) || allowLaterExpiry && a.HistorySealed && a.HistoryExpiredAt.IsZero() && !b.HistoryExpiredAt.IsZero()
}

// CollectionExecutionRetirementFence selects exactly one original result and
// retired prefix. It carries no resource payload, child identity or authority.
type CollectionExecutionRetirementFence struct {
	Result     CollectionExecutionResultState      `json:"result"`
	Retirement *CollectionExecutionRetirementState `json:"retirement,omitempty"`
}

func CollectionExecutionRetirementFenceFor(s CollectionState) *CollectionExecutionRetirementFence {
	if s.ExecutionResult == nil {
		return nil
	}
	f := &CollectionExecutionRetirementFence{Result: s.ExecutionResult.Clone()}
	if s.ExecutionRetirement != nil {
		r := s.ExecutionRetirement.Clone()
		f.Retirement = &r
	}
	return f
}

func (p CollectionExecutionRetirementFence) validate(binding CollectionExecutionBinding, at time.Time) error {
	if p.Result.Summary.validate() != nil || p.Result.validatePublication() != nil || p.Result.Summary.Binding != binding ||
		!p.Result.HistorySealed && p.Result.HistoryExpiredAt.IsZero() || at.Before(p.Result.Summary.FinalizedAt) || at.Before(p.Result.HistoryExpiredAt) {
		return ErrCollectionInvalid
	}
	if r := p.Retirement; r != nil {
		if r.Version != collectionExecutionRetirementVersion || !collectionExecutionResultStatesEqual(r.Result, p.Result, true) ||
			r.StartedAt.Before(r.Result.Summary.FinalizedAt) || r.UpdatedAt.Before(r.StartedAt) || at.Before(r.UpdatedAt) ||
			r.Checkpoint != nil && r.Checkpoint.validate() != nil || r.Sources != nil && r.Sources.validate() != nil {
			return ErrCollectionInvalid
		}
	}
	return nil
}

func collectionExecutionRetirementsEqual(a, b *CollectionExecutionRetirementState) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Version != b.Version || a.PreparedRemoved != b.PreparedRemoved || !a.StartedAt.Equal(b.StartedAt) ||
		!a.UpdatedAt.Equal(b.UpdatedAt) || !collectionExecutionResultStatesEqual(a.Result, b.Result, false) || (a.Checkpoint == nil) != (b.Checkpoint == nil) ||
		!collectionExecutionSourcesEqual(a.Sources, b.Sources) {
		return false
	}
	if a.Checkpoint == nil {
		return true
	}
	return a.Checkpoint.Version == b.Checkpoint.Version && collectionExecutionProgressEqual(a.Checkpoint.Progress, b.Checkpoint.Progress) && slices.Equal(a.Checkpoint.TerminalFrontier, b.Checkpoint.TerminalFrontier)
}
