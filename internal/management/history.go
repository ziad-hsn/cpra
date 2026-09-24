package management

import (
	"context"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type HistoryView struct {
	view    persistence.HistoryView
	catalog *Catalog
}

func (c *Catalog) HistorySnapshot() (HistoryView, error) {
	if !c.Ready() {
		return HistoryView{}, ErrUnavailable
	}
	view, err := c.store.History().Snapshot()
	return HistoryView{view: view, catalog: c}, err
}

// Page projects only public audit fields. Operation internals, provider outcome
// payloads and configuration records are not serialized into the v2 timeline.
func (v HistoryView) Page(ctx context.Context, monitorID, after string, limit int) ([]api.Event, string, error) {
	if v.catalog == nil || !v.catalog.Ready() {
		return nil, "", ErrUnavailable
	}
	if !validID(monitorID) {
		return nil, "", ErrValidation
	}
	rows, next, err := v.view.Page(ctx, monitorID, after, limit)
	if err != nil {
		return nil, "", err
	}
	items := make([]api.Event, 0, len(rows))
	for _, e := range rows {
		actor := e.Actor
		if actor == "" && e.Operation != nil {
			actor = e.Operation.Actor
		}
		event := api.Event{ID: e.ID, MonitorID: e.MonitorID, Kind: e.Type, Time: e.At, Actor: actor, Reason: e.Reason, Note: e.Note, IncidentID: e.IncidentID, ActionID: e.ActionID, Outcome: e.Outcome, ControlRevision: e.ControlRevision, EvidenceRefs: append([]string(nil), e.EvidenceRefs...)}
		if e.Operation == nil {
			event.ExecutionRevision = e.Revision
		} else {
			event.Outcome = e.Operation.Outcome
		}
		if e.ActionID != "" {
			event.ActionKind, event.Color = e.Kind, e.Color
			if e.Kind == "code" {
				event.Endpoint = api.Pointer(e.Endpoint)
			}
		}
		items = append(items, event)
	}
	return items, next, nil
}
