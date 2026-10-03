package management

import (
	"context"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type ActionView struct {
	view    persistence.ActionView
	catalog *Catalog
}

func actionView(a persistence.ActionRecord) api.Action {
	out := api.Action{ID: a.ID, MonitorID: a.MonitorID, IncarnationUID: a.CatalogUID, IncidentID: a.IncidentID, ExecutionID: a.ID, ExecutionRevision: a.Revision, State: string(a.State), Kind: a.Kind, Outcome: a.Outcome, ReviewRevision: a.ReviewRevision, Held: a.Held, ExecutorFenced: a.ExecutorFenced, ReceiptID: a.OperationID}
	out.AgentReceipt = agentReceiptView(a.AgentReceipt)
	out.RecoveryFailure = recoveryFailureView(a.RecoveryFailure)
	// NotBefore is a scheduling bound, not an action creation timestamp.
	updated := a.StartedAt
	if a.AgentReceipt != nil && a.AgentReceipt.ObservedAt.After(updated) {
		updated = a.AgentReceipt.ObservedAt
	}
	for _, at := range []time.Time{a.FinishedAt, a.ExecutorFinishedAt} {
		if at.After(updated) {
			updated = at
		}
	}
	if a.Review != nil {
		r := a.Review
		out.Review = &api.ActionReview{Revision: r.Revision, Resolution: api.ActionReviewResolution(r.Resolution), Actor: r.Actor, ReviewedAt: r.At, Reason: r.Reason, Note: r.Note, EvidenceRefs: append([]string(nil), r.EvidenceRefs...), Conflict: r.Conflict}
		if r.At.After(updated) {
			updated = r.At
		}
	}
	evidence := func(e *persistence.LateActionEvidence) *api.ActionEvidence {
		if e == nil {
			return nil
		}
		if e.RecordedAt.After(updated) {
			updated = e.RecordedAt
		}
		return &api.ActionEvidence{Outcome: e.Outcome, ExecutionStart: e.ExecutionStart, ExecutionEnd: e.ExecutionEnd, RecordedAt: e.RecordedAt}
	}
	out.LateEvidence = evidence(a.LateEvidence)
	out.ConflictingEvidence = evidence(a.ConflictingEvidence)
	if !updated.IsZero() {
		out.UpdatedAt = &updated
	}
	return out
}

func agentReceiptView(receipt *persistence.AgentReceipt) *api.AgentReceipt {
	if receipt == nil {
		return nil
	}
	out := &api.AgentReceipt{Sequence: strconv.FormatUint(receipt.Sequence, 10), ObservedAt: receipt.ObservedAt}
	if receipt.TaskID != "" {
		out.TaskID = api.Pointer(receipt.TaskID)
	}
	if receipt.ContextID != "" {
		out.ContextID = api.Pointer(receipt.ContextID)
	}
	if receipt.MessageID != "" {
		out.MessageID = api.Pointer(receipt.MessageID)
	}
	if receipt.State != "" {
		out.State = api.Pointer(receipt.State)
	}
	return out
}
func (c *Catalog) Action(ctx context.Context, id string) (api.Action, error) {
	if err := ctx.Err(); err != nil {
		return api.Action{}, err
	}
	if err := c.readyContext(ctx); err != nil {
		return api.Action{}, err
	}
	if !validID(id) {
		return api.Action{}, persistence.ErrCatalogNotFound
	}
	a, ok, err := c.store.ActionContext(ctx, id)
	if err != nil {
		return api.Action{}, err
	}
	if !ok {
		return api.Action{}, persistence.ErrCatalogNotFound
	}
	return actionView(a), nil
}
func (c *Catalog) ActionSnapshot() (ActionView, error) {
	return c.ActionSnapshotContext(context.Background())
}

func (c *Catalog) ActionSnapshotContext(ctx context.Context) (ActionView, error) {
	if err := c.readyContext(ctx); err != nil {
		return ActionView{}, err
	}
	v, err := c.store.ActionSnapshotContext(ctx)
	return ActionView{view: v, catalog: c}, err
}
func (v ActionView) Page(ctx context.Context, monitorID, after string, limit int) ([]api.Action, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if v.catalog == nil {
		return nil, "", ErrUnavailable
	}
	if err := v.catalog.readyContext(ctx); err != nil {
		return nil, "", err
	}
	if monitorID != "" && !validID(monitorID) {
		return nil, "", ErrValidation
	}
	var rows []persistence.ActionRecord
	var next string
	var err error
	if monitorID == "" {
		rows, next, err = v.view.Page(after, limit)
	} else {
		rows, next, err = v.view.PageByMonitor(monitorID, after, limit)
	}
	if err != nil {
		return nil, "", err
	}
	items := make([]api.Action, 0, len(rows))
	for _, a := range rows {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		items = append(items, actionView(a))
	}
	return items, next, nil
}

func recoveryFailureView(f *persistence.RecoveryFailure) *api.RecoveryFailure {
	if f == nil {
		return nil
	}
	return &api.RecoveryFailure{ActionID: f.ActionID, Driver: f.Driver, Attempt: f.Attempt, MaxAttempts: f.MaxAttempts, Trigger: api.RecoveryFailureTrigger(f.Trigger), Outcome: "failure", FailedAt: f.FailedAt}
}
