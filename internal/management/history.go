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
	return c.HistorySnapshotContext(context.Background())
}

func (c *Catalog) HistorySnapshotContext(ctx context.Context) (HistoryView, error) {
	if err := c.readyContext(ctx); err != nil {
		return HistoryView{}, err
	}
	view, err := c.store.History().SnapshotContext(ctx)
	return HistoryView{view: view, catalog: c}, err
}

// Page projects only public audit fields. Operation internals, provider outcome
// payloads and configuration records are not serialized into the v2 timeline.
func (v HistoryView) Page(ctx context.Context, monitorID, after string, limit int) ([]api.Event, string, error) {
	if v.catalog == nil {
		return nil, "", ErrUnavailable
	}
	if err := v.catalog.readyContext(ctx); err != nil {
		return nil, "", err
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
		event.Failure, event.Cause = failureView(e.Failure), failureView(e.Cause)
		event.AgentReceipt = agentReceiptView(e.AgentReceipt)
		event.RecoveryFailure = recoveryFailureView(e.RecoveryFailure)
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
