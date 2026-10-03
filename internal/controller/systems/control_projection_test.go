package systems

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestControlProjectionRefreshesHealthWithoutControlRevisionChange(t *testing.T) {
	s, catalog := projectionRecoveryOwner(t)
	resource := projectionRecoveryResource(t, "projection-health", "http://127.0.0.1:1/check")
	var spec api.MonitorSpec
	if err := json.Unmarshal(resource.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Enabled = api.Pointer(true)
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	resource.Spec = encoded
	resource = commitProjectionResource(t, catalog, resource)
	update := prepareRecoveryProjection(t, s, resource.Metadata.ID)
	s.startCatalogProjection(update)
	assertProjectionAck(t, update, nil)
	ent := s.entities[resource.Metadata.ID]
	before, ok := s.store.Get(resource.Metadata.ID)
	if !ok {
		t.Fatal("configured monitor missing")
	}
	guard := s.managed[resource.Metadata.ID].guard.Clone()
	at := time.Now().UTC()
	results, err := s.store.Submit(t.Context(), []persistence.Command{{Kind: "pulse", MonitorID: before.ID, Revision: before.Revision,
		CheckControlRevision: before.ControlRevision, Generation: before.Generation + 1, Outcome: "success", At: at, Guard: &guard}})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
		t.Fatalf("commit health: %v %+v", err, results)
	}
	current := *results[0].Monitor
	if current.ControlRevision != before.ControlRevision || current.IncidentRevision != before.IncidentRevision {
		t.Fatal("health update must not change control or incident version")
	}
	if row, _ := s.index.Get(ent.ID()); row.Uptime != 0 {
		t.Fatal("commit goroutine changed live dashboard projection")
	}
	change := persistence.ControlChange{MonitorID: before.ID, MonitorUID: before.CatalogUID, ControlRevision: before.ControlRevision}
	for range 2 {
		if err := s.projectControlChange(t.Context(), change); err != nil {
			t.Fatal(err)
		}
		row, _ := s.index.Get(ent.ID())
		state := s.states.Get(ent)
		if row.Uptime != 1 || !row.LastCheck.Equal(at) || state.PulseGeneration != current.Generation || !state.LastSuccessTime.Equal(at) {
			t.Fatal("owner did not publish committed health without a new control version")
		}
	}
	if len(s.store.SLOView(time.Now()).Reports) != 0 {
		t.Fatal("idempotent projection fabricated another SLO sample")
	}
	if got, _ := s.store.Get(before.ID); got.TotalChecks != 1 {
		t.Fatal("projection repeated a committed health transition")
	}
}

func TestControlProjectionRejectsForeignIncarnationAndCanceledRead(t *testing.T) {
	s, catalog := projectionRecoveryOwner(t)
	resource := commitProjectionResource(t, catalog, projectionRecoveryResource(t, "projection-fence", "http://127.0.0.1:1/check"))
	update := prepareRecoveryProjection(t, s, resource.Metadata.ID)
	s.startCatalogProjection(update)
	assertProjectionAck(t, update, nil)
	ent := s.entities[resource.Metadata.ID]
	before, _ := s.index.Get(ent.ID())
	err := s.projectControlChange(t.Context(), persistence.ControlChange{MonitorID: resource.Metadata.ID, MonitorUID: "old-incarnation"})
	if !errors.Is(err, persistence.ErrControlConflict) {
		t.Fatal("old worker hint crossed monitor incarnation", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = s.projectControlChange(ctx, persistence.ControlChange{MonitorID: resource.Metadata.ID, MonitorUID: resource.Metadata.UID})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("owner ignored canceled read", err)
	}
	after, _ := s.index.Get(ent.ID())
	if before.LastCheck != after.LastCheck || before.Uptime != after.Uptime || before.MonitorID != after.MonitorID {
		t.Fatal("failed observation changed dashboard")
	}
}
