package management

import (
	"context"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// CollectionExecutionResultView exposes the original protected result. The HTTP
// boundary checks current authentication and permissions before and after reads;
// storage checks original ownership and current health with this context.
func (c *Catalog) CollectionExecutionResultView(ctx context.Context, id, actor string, at time.Time) (*persistence.CollectionExecutionResultView, persistence.CollectionExecutionResultStatus, error) {
	if ctx == nil {
		return nil, persistence.CollectionExecutionResultStatus{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, persistence.CollectionExecutionResultStatus{}, err
	}
	if !c.verified.Load() || c.failed.Load() {
		return nil, persistence.CollectionExecutionResultStatus{}, ErrUnavailable
	}
	return c.store.CollectionExecutionResultView(ctx, id, actor, at)
}

func executionResultCounts(c persistence.CollectionExecutionCounts) api.ExecutionResultCounts {
	return api.ExecutionResultCounts{Processed: int64(c.Processed), Accepted: int64(c.Accepted), Unchanged: int64(c.Unchanged),
		Conflicts: int64(c.Conflicts), DependencyBlocked: int64(c.DependencyBlocked), Unattempted: int64(c.Unattempted),
		ChildPending: int64(c.ChildPending), ChildApplied: int64(c.ChildApplied), ChildFailed: int64(c.ChildFailed),
		ChildSuperseded: int64(c.ChildSuperseded), ChildInvalidated: int64(c.ChildInvalidated)}
}

func executionSummaryCounts(s persistence.CollectionExecutionSummary) api.ExecutionResultCounts {
	c := api.ExecutionResultCounts{Unattempted: int64(s.Unattempted)}
	if p := s.Fence.Progress; p != nil {
		c.Processed, c.Accepted, c.Unchanged = int64(p.Processed), int64(p.Accepted), int64(p.Unchanged)
		c.Conflicts, c.DependencyBlocked = int64(p.Conflicts), int64(p.DependencyBlocked)
		c.ChildPending, c.ChildApplied, c.ChildFailed = int64(p.Accepted-p.ChildTerminals), int64(p.ChildApplied), int64(p.ChildFailed)
		c.ChildSuperseded, c.ChildInvalidated = int64(p.ChildSuperseded), int64(p.ChildInvalidated)
	}
	return c
}

// CollectionExecutionReceiptResult converts an already verified immutable seal.
// It carries only metadata, and does not authorize access or verify availability.
// Cursor adapters use it to keep every page bound to the captured receipt.
func CollectionExecutionReceiptResult(r persistence.CollectionExecutionReceipt) api.ExecutionResultAvailability {
	s, d := r.Summary, r.Descriptor
	c := executionSummaryCounts(s)
	return api.ExecutionResultAvailability{State: "ready", Counts: &c, Summary: &api.ExecutionResultSummary{
		ResultID: s.Binding.ActivationID, UploadID: s.Binding.UploadID, PlanID: s.Binding.PlanID, PlanDigest: s.Binding.PlanDigest,
		Outcome: s.Outcome, ItemCount: int64(s.ItemCount), Processed: c.Processed, Accepted: c.Accepted, Unchanged: c.Unchanged,
		Conflicts: c.Conflicts, DependencyBlocked: c.DependencyBlocked, Unattempted: c.Unattempted,
		ChildPending: c.ChildPending, ChildApplied: c.ChildApplied, ChildFailed: c.ChildFailed,
		ChildSuperseded: c.ChildSuperseded, ChildInvalidated: c.ChildInvalidated,
		FinalizedAt: s.FinalizedAt, ExpiresAt: s.FinalizedAt.AddDate(0, 0, 30), Bytes: int64(d.Bytes), Digest: d.Digest}}
}

func collectionExecutionObservationResult(o persistence.CollectionExecutionObservation) api.ExecutionResultAvailability {
	c := executionResultCounts(o.Counts)
	r := api.ExecutionResultAvailability{State: o.State, Counts: &c}
	// The public summary binds a complete canonical descriptor, so an unsealed
	// finalization supplies counters without inventing a digest or byte count.
	if o.Summary != nil && o.Descriptor != nil {
		r = CollectionExecutionReceiptResult(persistence.CollectionExecutionReceipt{Summary: *o.Summary, Descriptor: *o.Descriptor})
		r.State = o.State
	}
	return r
}

// CollectionExecutionItemResult converts an already verified metadata-only row.
// Accepted remains committed even when its child failed. Only a recorded child
// disposition supplies applied; unchanged/rejected/unattempted have no child.
func CollectionExecutionItemResult(item persistence.CollectionExecutionItem) api.ApplyResult {
	r := api.ApplyResult{ID: item.Key.Kind + "/" + item.Key.ID, Kind: item.Key.Kind,
		InputOrdinal: api.Pointer(int64(item.InputOrdinal)), PlanOrdinal: api.Pointer(int64(item.PlanOrdinal)),
		Source: item.Source, SourceDocument: api.Pointer(int64(item.SourceDocument)), SourceItem: api.Pointer(int64(item.SourceItem)),
		OriginalUID: item.OriginalUID, OldVersion: item.OldVersion, UID: item.UID, NewVersion: item.NewVersion,
		CatalogDecision: item.Decision, Outcome: item.Decision, Committed: api.Pointer(item.Decision == "accepted")}
	if item.Generation != 0 {
		r.Generation = api.Pointer(int64(item.Generation))
	}
	if item.CommittedIndex != 0 {
		r.CommittedIndex = api.Pointer(int64(item.CommittedIndex))
	}
	if !item.DecidedAt.IsZero() {
		r.DecidedAt = api.Pointer(item.DecidedAt)
	}
	if child := item.Child; child != nil {
		r.ChildDisposition = &api.ExecutionChildDisposition{OperationID: child.ID, State: child.State, Outcome: child.Outcome,
			InvalidatedByRestore: child.InvalidatedByRestore}
		if !child.UpdatedAt.IsZero() {
			r.ChildDisposition.UpdatedAt = api.Pointer(child.UpdatedAt)
		}
		if child.State == "completed" || child.State == "failed" || child.State == "partial" {
			r.Applied = api.Pointer(child.State == "completed")
		}
	}
	return r
}
