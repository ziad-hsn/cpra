package management

import (
	"context"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Observations projects retained public state without a configuration catalog,
// encryption keys, mutation authority or provider clients. File installations
// use the same projections as managed installations.
type Observations struct{ store *persistence.Store }

func NewObservations(store *persistence.Store) *Observations { return &Observations{store: store} }

func (o *Observations) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.store == nil {
		return ErrUnavailable
	}
	status, err := o.store.StatusContext(ctx)
	if err != nil || !status.Ready {
		return ErrUnavailable
	}
	return nil
}

func (o *Observations) Incident(ctx context.Context, id string) (api.Incident, error) {
	if err := o.ready(ctx); err != nil {
		return api.Incident{}, err
	}
	if !validID(id) {
		return api.Incident{}, ErrValidation
	}
	record, ok, err := o.store.IncidentContext(ctx, id)
	if err != nil {
		return api.Incident{}, err
	}
	if !ok {
		return api.Incident{}, persistence.ErrCatalogNotFound
	}
	return incidentView(record), nil
}

func (o *Observations) IncidentSnapshotContext(ctx context.Context) (IncidentView, error) {
	if err := o.ready(ctx); err != nil {
		return IncidentView{}, err
	}
	view, err := o.store.IncidentSnapshotContext(ctx)
	return IncidentView{view: view, ready: o.ready}, err
}

func (o *Observations) Action(ctx context.Context, id string) (api.Action, error) {
	if err := o.ready(ctx); err != nil {
		return api.Action{}, err
	}
	if !validID(id) {
		return api.Action{}, ErrValidation
	}
	record, ok, err := o.store.ActionContext(ctx, id)
	if err != nil {
		return api.Action{}, err
	}
	if !ok {
		return api.Action{}, persistence.ErrCatalogNotFound
	}
	return actionView(record), nil
}

func (o *Observations) ActionSnapshotContext(ctx context.Context) (ActionView, error) {
	if err := o.ready(ctx); err != nil {
		return ActionView{}, err
	}
	view, err := o.store.ActionSnapshotContext(ctx)
	return ActionView{view: view, ready: o.ready}, err
}

func (o *Observations) HistorySnapshotContext(ctx context.Context) (HistoryView, error) {
	if err := o.ready(ctx); err != nil {
		return HistoryView{}, err
	}
	view, err := o.store.History().SnapshotContext(ctx)
	return HistoryView{view: view, ready: o.ready}, err
}
