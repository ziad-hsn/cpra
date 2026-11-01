package management

import (
	"context"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type ActionView struct {
	view  persistence.ActionView
	ready func(context.Context) error
}

func actionView(a persistence.ActionRecord) api.Action {
	out := api.Action{ID: a.ID, MonitorID: a.MonitorID, IncarnationUID: a.IncarnationID(), IncidentID: a.IncidentID, ExecutionID: a.ID, ExecutionRevision: a.Revision, State: string(a.State), Kind: a.Kind, Outcome: a.Outcome, ReviewRevision: a.ReviewRevision, Held: a.Held, ExecutorFenced: a.ExecutorFenced, ReceiptID: a.OperationID}
	out.RecoverySelection = a.Rule.Public()
	out.TerminationReason, out.Resolution = a.TerminationReason, resolutionView(a.Resolution)
	if a.ExecutionBudget > 0 {
		out.ExecutionBudget = a.ExecutionBudget.String()
	}
	out.Failure, out.Cause = failureView(a.Failure), failureView(a.Cause)
	out.Color, out.DeliveryID, out.EndpointID, out.NotificationEpisode = a.Color, a.DeliveryID, a.EndpointID, a.NotificationEpisode
	if a.ReviewNotice != nil {
		n := a.ReviewNotice
		out.ReviewNotice = &api.ReviewNotice{AlertID: n.AlertID, ActionID: n.ActionID, Phase: n.Phase, Reason: n.Reason, Health: n.Health}
	}
	out.AgentReceipt = agentReceiptView(a.AgentReceipt)
	out.RecoveryFailure = recoveryFailureView(a.RecoveryFailure)
	utc := func(at time.Time) *time.Time {
		if at.IsZero() {
			return nil
		}
		value := at.UTC()
		return &value
	}
	out.CreatedAt, out.QueuedAt = utc(a.QueuedAt), utc(a.QueuedAt)
	out.NotBefore, out.Deadline = utc(a.NotBefore), utc(a.Deadline)
	out.StartedAt, out.FinishedAt = utc(a.StartedAt), utc(a.FinishedAt)
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
		out.Review = &api.ActionReview{Revision: r.Revision, Resolution: api.ActionReviewResolution(r.Resolution), Actor: r.Actor, ReviewedAt: r.At.UTC(), Reason: r.Reason, Note: r.Note, EvidenceRefs: append([]string(nil), r.EvidenceRefs...), Conflict: r.Conflict}
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
		return &api.ActionEvidence{Outcome: e.Outcome, ExecutionStart: e.ExecutionStart.UTC(), ExecutionEnd: e.ExecutionEnd.UTC(), RecordedAt: e.RecordedAt.UTC()}
	}
	out.LateEvidence = evidence(a.LateEvidence)
	out.ConflictingEvidence = evidence(a.ConflictingEvidence)
	if !updated.IsZero() {
		updated = updated.UTC()
		out.UpdatedAt = &updated
	}
	return out
}

func agentReceiptView(receipt *persistence.AgentReceipt) *api.AgentReceipt {
	if receipt == nil {
		return nil
	}
	out := &api.AgentReceipt{Sequence: strconv.FormatUint(receipt.Sequence, 10), ObservedAt: receipt.ObservedAt.UTC()}
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
	return ActionView{view: v, ready: c.readyContext}, err
}

func (v ActionView) Retain() (ActionView, error) {
	view, err := v.view.Retain()
	return ActionView{view: view, ready: v.ready}, err
}

func (v ActionView) Close() error { return v.view.Close() }
func (v ActionView) Page(ctx context.Context, monitorID, after string, limit int) ([]api.Action, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if v.ready == nil {
		return nil, "", ErrUnavailable
	}
	if err := v.ready(ctx); err != nil {
		return nil, "", err
	}
	if monitorID != "" && !validID(monitorID) {
		return nil, "", ErrValidation
	}
	rows, next, err := v.view.PageContext(ctx, monitorID, after, limit)
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
	return &api.RecoveryFailure{ActionID: f.ActionID, Driver: f.Driver, Attempt: f.Attempt, MaxAttempts: f.MaxAttempts, TerminationReason: f.TerminationReason, Trigger: api.RecoveryFailureTrigger(f.Trigger), Outcome: "failure", FailedAt: f.FailedAt.UTC()}
}

func resolutionView(n *persistence.ResolutionNotice) *api.ResolutionNotice {
	if n == nil {
		return nil
	}
	return &api.ResolutionNotice{ConditionID: n.ConditionID, RecoveredAt: n.RecoveredAt.UTC(), Health: api.ResolutionNoticeHealth(n.Health)}
}
