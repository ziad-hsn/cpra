package management

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCatalogObservationsRejectCanceledContextWithoutChangingState(t *testing.T) {
	c, store, _, _, monitor := reconciliationContextFixture(t)
	actions, err := c.ActionSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	incidents, err := c.IncidentSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	history, err := c.HistorySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	before := store.Status().CommittedIndex
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, read := range []struct {
		name string
		run  func() error
	}{
		{"resource", func() error {
			r, err := c.Get(ctx, "Monitor", monitor.Metadata.ID)
			if !reflect.DeepEqual(r, api.Resource{}) {
				t.Fatal("cancelled resource read returned data")
			}
			return err
		}},
		{"action", func() error { _, err := c.Action(ctx, "missing-action"); return err }},
		{"incident", func() error { _, err := c.Incident(ctx, "missing-incident"); return err }},
		{"action snapshot", func() error { _, err := c.ActionSnapshotContext(ctx); return err }},
		{"incident snapshot", func() error { _, err := c.IncidentSnapshotContext(ctx); return err }},
		{"history snapshot", func() error { _, err := c.HistorySnapshotContext(ctx); return err }},
		{"action page", func() error { _, _, err := actions.Page(ctx, "", "", 100); return err }},
		{"incident page", func() error { _, _, err := incidents.Page(ctx, "", "", 100); return err }},
		{"history page", func() error { _, _, err := history.Page(ctx, monitor.Metadata.ID, "", 100); return err }},
	} {
		t.Run(read.name, func(t *testing.T) {
			if err := read.run(); !errors.Is(err, context.Canceled) {
				t.Fatal("read lost caller cancellation", err)
			}
		})
	}
	if store.Status().CommittedIndex != before || !c.Ready() {
		t.Fatal("cancelled observation changed committed state or readiness")
	}
	if _, err := c.Get(t.Context(), "Monitor", monitor.Metadata.ID); err != nil {
		t.Fatal("cancelled observation prevented subsequent read", err)
	}
}
