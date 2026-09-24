package systems

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func projectionRecoveryOwner(t *testing.T) (*DurableSystem, *management.Catalog) {
	t.Helper()
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	store, err := persistence.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{71}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	world := ecs.NewWorld()
	s := NewDurableSystem(&world, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, nil, nil, nil, time.Minute, false)
	s.Initialize(&world)
	if s.lastError != nil {
		t.Fatal(s.lastError)
	}
	// Keep the owner and reconciler handoff deterministic; preparation and
	// mutations still use the real encrypted catalog and persistence machine.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.catalogRuntime = &catalogRuntime{catalog: catalog, store: store, mapper: entities.NewEntityManager(&world), known: map[string]bool{}, updates: make(chan catalogProjection, 1), ctx: ctx, cancel: cancel}
	s.managed = make(map[string]managedMonitor)
	return s, catalog
}

func projectionRecoveryResource(t *testing.T, id, target string) api.Resource {
	t.Helper()
	driver, err := json.Marshal(map[string]string{"url": target})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(api.MonitorSpec{Enabled: api.Pointer(false), Check: api.CheckSpec{Interval: "1h", Timeout: "1s", UnhealthyThreshold: api.Pointer(int64(1)), HealthyThreshold: api.Pointer(int64(1)), Driver: api.DriverConfig{Type: "http", Config: driver}}})
	if err != nil {
		t.Fatal(err)
	}
	return api.Resource{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: id, Name: &id}, Spec: spec}
}

func commitProjectionResource(t *testing.T, catalog *management.Catalog, resource api.Resource) api.Resource {
	t.Helper()
	prepared, err := catalog.Prepare(context.Background(), resource, resource.Metadata.ResourceVersion, resource.Metadata.ResourceVersion == "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalog.CommitAs(context.Background(), prepared, "projection-operator")
	if err != nil {
		t.Fatal(err)
	}
	return result.Resource
}

func prepareRecoveryProjection(t *testing.T, s *DurableSystem, id string) catalogProjection {
	t.Helper()
	view, err := s.catalogRuntime.catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.catalogRuntime.prepare(context.Background(), view, id)
	if err != nil {
		t.Fatal(err)
	}
	p.done = make(chan error, 1)
	t.Cleanup(func() { _ = p.prepared.Close() })
	return p
}

func assertProjectionAck(t *testing.T, update catalogProjection, want error) {
	t.Helper()
	select {
	case err := <-update.done:
		if !errors.Is(err, want) {
			t.Fatalf("projection acknowledgement = %v, want %v", err, want)
		}
	default:
		t.Fatal("resolved projection was not acknowledged")
	}
	assertProjectionUnacknowledged(t, update)
}

func assertProjectionUnacknowledged(t *testing.T, update catalogProjection) {
	t.Helper()
	select {
	case err := <-update.done:
		t.Fatalf("unexpected projection acknowledgement: %v", err)
	default:
	}
}

func assertProjectionObserved(t *testing.T, catalog *management.Catalog, id string, want int64) {
	t.Helper()
	resource, err := catalog.Get(context.Background(), "Monitor", id)
	if err != nil {
		t.Fatal(err)
	}
	var status api.MonitorStatus
	if err := json.Unmarshal(resource.Status, &status); err != nil {
		t.Fatal(err)
	}
	if status.ObservedGeneration != want {
		t.Fatalf("observed generation = %d, want %d", status.ObservedGeneration, want)
	}
}

func recoverProjectionWrite(t *testing.T, s *DurableSystem, writer *lostOwnerReply) {
	t.Helper()
	writer.holdBarrier.Store(false)
	s.nextRecovery = time.Time{}
	if !s.recoverWrites(time.Now()) || s.pendingWrite != nil || s.lastError != nil {
		t.Fatalf("projection write did not settle: %v", s.lastError)
	}
	if len(writer.writes) != 2 || !bytes.Equal(writer.writes[0], writer.writes[1]) {
		t.Fatal("recovery changed the original command identity or timestamp")
	}
}

func TestOwnerProjectionRecoveryRetainsPreparedConfigure(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed_%t", committed), func(t *testing.T) {
			s, catalog := projectionRecoveryOwner(t)
			resource := commitProjectionResource(t, catalog, projectionRecoveryResource(t, "configured", "http://127.0.0.1:1/original"))
			update := prepareRecoveryProjection(t, s, resource.Metadata.ID)
			writer := &lostOwnerReply{store: s.store, kind: "configure", commitFirst: committed}
			s.committer = writer
			s.startCatalogProjection(update)
			if s.pendingWrite == nil || s.pendingWrite.projection.prepared != update.prepared || len(s.entities) != 0 || s.index.Overview().Total != 0 {
				t.Fatal("uncertain configure lost its original preparation or installed early")
			}
			assertProjectionUnacknowledged(t, update)
			assertProjectionObserved(t, catalog, resource.Metadata.ID, 0)
			writer.holdBarrier.Store(true)
			if s.recoverWrites(time.Now()) || len(writer.writes) != 1 || s.pendingWrite.projection.prepared != update.prepared {
				t.Fatal("unconfirmed barrier released the preparation")
			}
			assertProjectionUnacknowledged(t, update)
			recoverProjectionWrite(t, s, writer)
			assertProjectionAck(t, update, nil)
			ent, ok := s.entities[resource.Metadata.ID]
			row, indexed := s.index.Get(ent.ID())
			if !ok || !s.world.Alive(ent) || !indexed || row.Target != "http://127.0.0.1:1/original" || s.jobs.Get(ent).PulseJob == nil || s.managed[resource.Metadata.ID].uid != resource.Metadata.UID {
				t.Fatal("original prepared jobs were not installed into the owner")
			}
			assertProjectionObserved(t, catalog, resource.Metadata.ID, resource.Metadata.Generation)
		})
	}
}

func TestOwnerProjectionRecoveryRejectsSupersededConfigure(t *testing.T) {
	s, catalog := projectionRecoveryOwner(t)
	resource := commitProjectionResource(t, catalog, projectionRecoveryResource(t, "superseded", "http://127.0.0.1:1/old"))
	old := prepareRecoveryProjection(t, s, resource.Metadata.ID)
	writer := &lostOwnerReply{store: s.store, kind: "configure", commitFirst: true}
	s.committer = writer
	s.startCatalogProjection(old)
	assertProjectionUnacknowledged(t, old)
	resource.Spec = projectionRecoveryResource(t, resource.Metadata.ID, "http://127.0.0.1:1/new").Spec
	resource = commitProjectionResource(t, catalog, resource)
	recoverProjectionWrite(t, s, writer)
	assertProjectionAck(t, old, persistence.ErrCatalogDependency)
	if len(s.entities) != 0 || s.index.Overview().Total != 0 {
		t.Fatal("superseded preparation became a live projection")
	}
	assertProjectionObserved(t, catalog, resource.Metadata.ID, 0)
	unrelated := ecs.NewWorld()
	if _, err := entities.NewEntityManager(&unrelated).Install(old.prepared, &unrelated); err == nil {
		t.Fatal("rejected preparation retained usable private jobs")
	}
	current := prepareRecoveryProjection(t, s, resource.Metadata.ID)
	s.startCatalogProjection(current)
	assertProjectionAck(t, current, nil)
	ent := s.entities[resource.Metadata.ID]
	row, ok := s.index.Get(ent.ID())
	if !ok || row.Target != "http://127.0.0.1:1/new" || s.states.Get(ent).Revision != current.runtime.ExecutionRevision {
		t.Fatal("new configuration was not installed after stale guard rejection")
	}
	assertProjectionObserved(t, catalog, resource.Metadata.ID, resource.Metadata.Generation)
}

func deleteRecoveryMonitor(t *testing.T, s *DurableSystem, resource api.Resource) catalogProjection {
	t.Helper()
	prepared, err := s.catalogRuntime.catalog.PrepareDelete(context.Background(), "Monitor", resource.Metadata.ID, resource.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.catalogRuntime.catalog.CommitAs(context.Background(), prepared, "projection-operator"); err != nil {
		t.Fatal(err)
	}
	record, ok, err := s.store.CatalogRetained(persistence.CatalogKey{Kind: "Monitor", ID: resource.Metadata.ID})
	if err != nil || !ok || !record.Removed {
		t.Fatalf("missing committed tombstone: %v", err)
	}
	return catalogProjection{removed: &record, done: make(chan error, 1)}
}

func TestOwnerProjectionRecoveryRemovesOnlyOriginalIncarnation(t *testing.T) {
	for _, recreated := range []bool{false, true} {
		t.Run(fmt.Sprintf("recreated_%t", recreated), func(t *testing.T) {
			s, catalog := projectionRecoveryOwner(t)
			resource := commitProjectionResource(t, catalog, projectionRecoveryResource(t, "removed", "http://127.0.0.1:1/old"))
			initial := prepareRecoveryProjection(t, s, resource.Metadata.ID)
			s.startCatalogProjection(initial)
			assertProjectionAck(t, initial, nil)
			oldEntity := s.entities[resource.Metadata.ID]
			removed := deleteRecoveryMonitor(t, s, resource)
			writer := &lostOwnerReply{store: s.store, kind: "remove", commitFirst: true}
			s.committer = writer
			s.startCatalogProjection(removed)
			if s.pendingWrite == nil || !s.world.Alive(oldEntity) || s.index.Overview().Total != 1 {
				t.Fatal("uncertain removal discarded its live owner projection")
			}
			assertProjectionUnacknowledged(t, removed)
			if recreated {
				replacement := projectionRecoveryResource(t, resource.Metadata.ID, "http://127.0.0.1:1/replacement")
				resource = commitProjectionResource(t, catalog, replacement)
				if resource.Metadata.UID == removed.removed.UID {
					t.Fatal("fixture reused the deleted incarnation")
				}
			}
			recoverProjectionWrite(t, s, writer)
			if !recreated {
				assertProjectionAck(t, removed, nil)
				if s.world.Alive(oldEntity) || len(s.entities) != 0 || s.index.Overview().Total != 0 {
					t.Fatal("committed removal left a live ECS projection")
				}
				return
			}
			assertProjectionAck(t, removed, persistence.ErrCatalogDependency)
			current := prepareRecoveryProjection(t, s, resource.Metadata.ID)
			s.startCatalogProjection(current)
			assertProjectionAck(t, current, nil)
			replacementEntity := s.entities[resource.Metadata.ID]
			// Redelivery of the old tombstone must not remove the replacement.
			removed.done = make(chan error, 1)
			s.startCatalogProjection(removed)
			assertProjectionAck(t, removed, persistence.ErrCatalogDependency)
			row, ok := s.index.Get(replacementEntity.ID())
			if s.world.Alive(oldEntity) || !s.world.Alive(replacementEntity) || !ok || row.Target != "http://127.0.0.1:1/replacement" || s.managed[resource.Metadata.ID].uid != resource.Metadata.UID {
				t.Fatal("stale removal affected the replacement incarnation")
			}
			assertProjectionObserved(t, catalog, resource.Metadata.ID, resource.Metadata.Generation)
		})
	}
}

func commitRecoverySnooze(t *testing.T, s *DurableSystem, id, duration string) persistence.Monitor {
	t.Helper()
	m, ok := s.store.Get(id)
	if !ok {
		t.Fatal("missing configured monitor")
	}
	prepared, err := s.catalogRuntime.catalog.PrepareControl(context.Background(), "snooze", id, api.ControlRequest{Revision: m.ControlRevision, Duration: duration, Reason: "maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.catalogRuntime.catalog.CommitControlAs(context.Background(), prepared, "projection-operator"); err != nil {
		t.Fatal(err)
	}
	m, _ = s.store.Get(id)
	return m
}

func TestOwnerProjectionRecoveryFreezesSnoozeExpiry(t *testing.T) {
	for _, committed := range []bool{false, true} {
		for _, newerControl := range []bool{false, true} {
			t.Run(fmt.Sprintf("committed_%t_newer_control_%t", committed, newerControl), func(t *testing.T) {
				s, catalog := projectionRecoveryOwner(t)
				resource := commitProjectionResource(t, catalog, projectionRecoveryResource(t, "paused", "http://127.0.0.1:1/paused"))
				initial := prepareRecoveryProjection(t, s, resource.Metadata.ID)
				s.startCatalogProjection(initial)
				assertProjectionAck(t, initial, nil)
				paused := commitRecoverySnooze(t, s, resource.Metadata.ID, "1h")
				if err := s.projectControlChange(context.Background(), persistence.ControlChange{MonitorID: paused.ID, MonitorUID: paused.CatalogUID}); err != nil {
					t.Fatal(err)
				}
				writer := &lostOwnerReply{store: s.store, kind: "control", commitFirst: committed}
				s.committer = writer
				s.expireSnoozes(paused.SnoozedUntil)
				if s.pendingWrite == nil || s.pendingWrite.kind != expiryWrite || len(s.pendingWrite.commands) != 1 || s.snoozes.Len() != 0 {
					t.Fatal("uncertain expiry lost its popped deadline or frozen command")
				}
				command := s.pendingWrite.commands[0]
				frozen := *command.Control
				if frozen.Action != "expire_snooze" || frozen.ExpectedRevision != paused.ControlRevision || frozen.MonitorUID != paused.CatalogUID || !frozen.Until.Equal(paused.SnoozedUntil) || frozen.OperationID == "" || frozen.OperationID != frozen.Revision {
					t.Fatal("expiry did not retain the original CAS and operation identity")
				}
				writer.holdBarrier.Store(true)
				if s.recoverWrites(time.Now()) || len(writer.writes) != 1 {
					t.Fatal("uncertain barrier permitted another expiry")
				}
				var newer persistence.Monitor
				if newerControl {
					newer = commitRecoverySnooze(t, s, paused.ID, "2h")
				}
				recoverProjectionWrite(t, s, writer)
				current, _ := s.store.Get(paused.ID)
				ent := s.entities[paused.ID]
				projected := s.controls.Get(ent)
				if projected.Revision != current.ControlRevision || !projected.SnoozedUntil.Equal(current.SnoozedUntil) || current.Policy.Enabled {
					t.Fatal("expiry reconciliation lost committed controls or enabled the monitor")
				}
				if newerControl {
					position, scheduled := s.snoozes.positions[ent]
					if current.ControlRevision != newer.ControlRevision || !current.SnoozedUntil.Equal(newer.SnoozedUntil) || !scheduled || !s.snoozes.entries[position].at.Equal(newer.SnoozedUntil) {
						t.Fatal("old expiry overwrote or lost the newer snooze")
					}
				} else if !current.SnoozedUntil.IsZero() || current.ControlRevision != frozen.Revision || s.snoozes.Len() != 0 {
					t.Fatal("confirmed expiry was not projected")
				}
				page, err := s.store.History().Page(paused.ID, "", 100)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, event := range page.Events {
					if event.Type == "control_expire_snooze" {
						count++
						if event.ControlRevision != frozen.Revision || !event.At.Equal(command.At) || event.Actor != "system" || event.CatalogUID != paused.CatalogUID {
							t.Fatal("expiry history changed frozen identity or observation time")
						}
					}
				}
				want := 1
				if !committed && newerControl {
					want = 0
				}
				if count != want {
					t.Fatalf("expiry events = %d, want %d", count, want)
				}
			})
		}
	}
}

type corruptedOwnerReply struct {
	*lostOwnerReply
	corrupt func(*persistence.Result, persistence.Command)
}

func (w *corruptedOwnerReply) Submit(ctx context.Context, commands []persistence.Command) ([]persistence.Result, error) {
	results, err := w.lostOwnerReply.Submit(ctx, commands)
	if err == nil && len(results) > 0 && commands[0].Kind == w.kind {
		i := len(results) - 1
		if results[i].Monitor != nil {
			monitor := results[i].Monitor.Clone()
			results[i].Monitor = &monitor
		}
		w.corrupt(&results[i], commands[i])
	}
	return results, err
}

func TestOwnerExpiryMalformedReplyRetainsEntireProjectionBatch(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(*persistence.Result, persistence.Command)
	}{
		{"missing_monitor", func(result *persistence.Result, _ persistence.Command) { result.Monitor = nil }},
		{"different_monitor", func(result *persistence.Result, _ persistence.Command) { result.Monitor.ID = "unrelated" }},
		{"different_incarnation", func(result *persistence.Result, _ persistence.Command) { result.Monitor.CatalogUID = "unrelated" }},
		{"different_control_revision", func(result *persistence.Result, command persistence.Command) {
			result.Monitor.ControlRevision = command.Control.ExpectedRevision
		}},
		{"still_snoozed", func(result *persistence.Result, command persistence.Command) {
			result.Monitor.SnoozedUntil = command.Control.Until
		}},
		{"removed_monitor", func(result *persistence.Result, _ persistence.Command) { result.Monitor.Removed = true }},
		{"error_with_monitor", func(result *persistence.Result, _ persistence.Command) { result.Err = persistence.ErrControlConflict }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s, catalog := projectionRecoveryOwner(t)
			before := make(map[string]persistence.Monitor)
			var expiry time.Time
			for _, id := range []string{"first-expiry", "second-expiry"} {
				resource := commitProjectionResource(t, catalog, projectionRecoveryResource(t, id, "http://127.0.0.1:1/paused"))
				initial := prepareRecoveryProjection(t, s, resource.Metadata.ID)
				s.startCatalogProjection(initial)
				assertProjectionAck(t, initial, nil)
				paused := commitRecoverySnooze(t, s, id, "1h")
				before[id] = paused
				if paused.SnoozedUntil.After(expiry) {
					expiry = paused.SnoozedUntil
				}
				if err := s.projectControlChange(context.Background(), persistence.ControlChange{MonitorID: id, MonitorUID: paused.CatalogUID}); err != nil {
					t.Fatal(err)
				}
			}
			writer := &corruptedOwnerReply{lostOwnerReply: &lostOwnerReply{store: s.store, kind: "control"}, corrupt: test.corrupt}
			s.committer = writer
			s.expireSnoozes(expiry)
			pending := s.pendingWrite
			if pending == nil || len(pending.commands) != 2 || len(pending.expiries) != 2 || s.snoozes.Len() != 0 {
				t.Fatal("fixture did not retain both popped deadlines")
			}
			// Recovery commits both real controls, then receives a valid first row
			// and a malformed second row. Neither local projection may be released.
			if s.recoverWrites(time.Now()) || s.pendingWrite != pending || pending.completed != 0 || s.lastError == nil || s.AdmissionReady() {
				t.Fatal("malformed reply released pending expiry ownership or admission")
			}
			if len(pending.expiries) != 2 || s.snoozes.Len() != 0 || len(writer.writes) != 2 || !bytes.Equal(writer.writes[0], writer.writes[1]) {
				t.Fatal("malformed reply lost or regenerated a retained deadline")
			}
			for id, original := range before {
				current, ok := s.store.Get(id)
				if !ok || !current.SnoozedUntil.IsZero() {
					t.Fatal("fixture failed to commit the real expiry before corrupting its reply")
				}
				projected := s.controls.Get(s.entities[id])
				if projected.Revision != original.ControlRevision || !projected.SnoozedUntil.Equal(original.SnoozedUntil) {
					t.Fatal("an earlier valid row was projected before the whole reply was validated")
				}
			}
		})
	}
}

func TestOwnerConfigureMalformedReplyRetainsPreparation(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(*persistence.Result, persistence.Command)
	}{
		{"missing_monitor", func(result *persistence.Result, _ persistence.Command) { result.Monitor = nil }},
		{"different_monitor", func(result *persistence.Result, _ persistence.Command) { result.Monitor.ID = "unrelated" }},
		{"different_incarnation", func(result *persistence.Result, _ persistence.Command) { result.Monitor.CatalogUID = "unrelated" }},
		{"different_catalog_revision", func(result *persistence.Result, _ persistence.Command) { result.Monitor.CatalogRevision = "unrelated" }},
		{"different_execution_revision", func(result *persistence.Result, _ persistence.Command) { result.Monitor.Revision = "unrelated" }},
		{"removed_monitor", func(result *persistence.Result, _ persistence.Command) { result.Monitor.Removed = true }},
		{"error_with_monitor", func(result *persistence.Result, _ persistence.Command) { result.Err = persistence.ErrCatalogDependency }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s, catalog := projectionRecoveryOwner(t)
			resource := commitProjectionResource(t, catalog, projectionRecoveryResource(t, "malformed-configure", "http://127.0.0.1:1/original"))
			update := prepareRecoveryProjection(t, s, resource.Metadata.ID)
			writer := &corruptedOwnerReply{lostOwnerReply: &lostOwnerReply{store: s.store, kind: "configure"}, corrupt: test.corrupt}
			s.committer = writer
			s.startCatalogProjection(update)
			pending := s.pendingWrite
			if pending == nil || pending.projection.prepared != update.prepared {
				t.Fatal("fixture did not retain the original preparation")
			}
			assertProjectionObserved(t, catalog, resource.Metadata.ID, 0)
			if s.recoverWrites(time.Now()) || s.pendingWrite != pending || pending.installed || s.lastError == nil || s.AdmissionReady() {
				t.Fatal("malformed configure reply released preparation ownership or admission")
			}
			assertProjectionUnacknowledged(t, update)
			if len(s.entities) != 0 || len(s.managed) != 0 || s.index.Overview().Total != 0 || pending.projection.prepared != update.prepared {
				t.Fatal("malformed configure reply installed or replaced runtime jobs")
			}
			if len(writer.writes) != 2 || !bytes.Equal(writer.writes[0], writer.writes[1]) {
				t.Fatal("configure recovery changed the frozen command")
			}
			current, ok := s.store.Get(resource.Metadata.ID)
			if !ok || current.CatalogUID != resource.Metadata.UID || current.Revision != update.runtime.ExecutionRevision || current.CatalogRevision != resource.Metadata.ResourceVersion || current.Removed {
				t.Fatal("fixture corrupted committed state instead of its response")
			}
			// An isolated install proves that the original private bundle survived.
			probe := ecs.NewWorld()
			manager := entities.NewEntityManager(&probe)
			ent, err := manager.Install(update.prepared, &probe)
			if err != nil || manager.JobStorage.Get(ent).PulseJob == nil {
				t.Fatalf("malformed reply prematurely released the original preparation: %v", err)
			}
		})
	}
}
