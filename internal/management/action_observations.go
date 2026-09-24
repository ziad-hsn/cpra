package management

import (
	"context"
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
	// NotBefore is a scheduling bound, not an action creation timestamp.
	updated := a.StartedAt
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
func (c *Catalog) Action(ctx context.Context, id string) (api.Action, error) {
	if err := ctx.Err(); err != nil {
		return api.Action{}, err
	}
	if !c.Ready() {
		return api.Action{}, ErrUnavailable
	}
	if !validID(id) {
		return api.Action{}, persistence.ErrCatalogNotFound
	}
	a, ok, err := c.store.Action(id)
	if err != nil {
		return api.Action{}, err
	}
	if !ok {
		return api.Action{}, persistence.ErrCatalogNotFound
	}
	return actionView(a), nil
}
func (c *Catalog) ActionSnapshot() (ActionView, error) {
	if !c.Ready() {
		return ActionView{}, ErrUnavailable
	}
	v, err := c.store.ActionSnapshot()
	return ActionView{view: v, catalog: c}, err
}
func (v ActionView) Page(ctx context.Context, monitorID, after string, limit int) ([]api.Action, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if v.catalog == nil || !v.catalog.Ready() {
		return nil, "", ErrUnavailable
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
