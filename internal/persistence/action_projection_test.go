package persistence

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"
)

// Seed a supported pre-projection image: attempts already retired by the 44/45
// retry path are absent from Monitor.Actions but remain in the ledger.
func seedArchivedAttempts(t *testing.T, count int) (*Store, Monitor) {
	t.Helper()
	s := openCatalogMemory(t)
	configure(t, s)
	s.fsm.mu.Lock()
	defer s.fsm.mu.Unlock()
	f := s.fsm
	m := f.image.Monitors["stable-one"]
	m.Policy.Endpoints = nil
	m.ActionProjection = nil
	m.Actions = map[string]Action{}
	f.image.Monitors[m.ID] = m
	f.image.ActionProjectionState = nil
	if count == 0 {
		return s, m
	}
	if err := f.ensureCollectionLedger(); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	for n := 0; n < count; {
		rows := make([]actionLedgerRow, 0, 64)
		for len(rows) < 64 && n < count {
			id := fmt.Sprintf("historical-%06d", n)
			rows = append(rows, actionLedgerRow{MonitorID: m.ID, Action: Action{ID: id, FileIncarnation: m.FileIncarnation, Revision: m.Revision, Kind: "intervention", IncidentID: "past-incident", Attempt: 1, State: Failed, Outcome: "rejected", NotBefore: at, StartedAt: at, FinishedAt: at.Add(time.Second)}})
			n++
		}
		position := actionLedgerPosition{Index: f.image.Index + 1}
		sequence, err := f.collections.CommitActions(position, rows)
		if err != nil {
			t.Fatal(err)
		}
		f.image.Index = position.Index
		f.image.ActionStorage = &actionStorageState{Sequence: sequence, Position: position}
	}
	return s, m
}
func projectionSnapshot(t *testing.T, s *Store) []byte {
	t.Helper()
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	sink := &collectionTestSink{}
	if err = frozen.Persist(sink); err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(sink.Bytes())
}
func TestActionProjectionHistoricalGrowthDoesNotEnterChecks(t *testing.T) {
	var baseline float64
	for _, count := range []int{0, 500, 5000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, m := seedArchivedAttempts(t, count)
			if err := s.migrateActionProjection(t.Context()); err != nil {
				t.Fatal(err)
			}
			current, _ := s.Get(m.ID)
			if current.ActionProjection == nil || len(current.Actions) != 0 || current.ActionProjection.LatestRecovery != nil {
				t.Fatal("history retained in check projection")
			}
			encoded := projectionSnapshot(t, s)
			i, ledger, err := decodeSnapshot(bytes.NewReader(encoded), "")
			if err != nil {
				t.Fatal("snapshot", err)
			}
			if ledger != nil {
				defer ledger.Close()
			}
			if !i.ActionProjectionState.Ready || len(i.Monitors[m.ID].Actions) != 0 {
				t.Fatal("projection cutover lost")
			}
			seen := 0
			if ledger != nil {
				pin, err := ledger.pinActions()
				if err != nil {
					t.Fatal(err)
				}
				defer pin.Close()
				after := ""
				for {
					rows, next, err := pin.Page(t.Context(), m.ID, after, 100)
					if err != nil {
						t.Fatal(err)
					}
					seen += len(rows)
					if next == "" {
						break
					}
					after = next
				}
			}
			if seen != count {
				t.Fatalf("retained inventory %d, want %d", seen, count)
			}
			// Remove ledger access while applying an ordinary healthy observation.
			// Any historical read/write would fail rather than hide behind a warm cache.
			s.fsm.mu.Lock()
			saved := s.fsm.collections
			s.fsm.collections = nil
			s.fsm.mu.Unlock()
			result := applyCatalogFormat(t, s, ActionFormatVersion, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "success", At: time.Now().UTC()})[0]
			s.fsm.mu.Lock()
			s.fsm.collections = saved
			s.fsm.mu.Unlock()
			if result.Err != nil || result.Monitor == nil || result.Monitor.TotalChecks != 1 || len(result.Monitor.Actions) != 0 {
				t.Fatal("check accessed retained history", result.Err)
			}
			allocations := testing.AllocsPerRun(100, func() { _ = current.Clone() })
			if count == 0 {
				baseline = allocations
			} else if allocations != baseline {
				t.Fatalf("projection clone allocations grew: %g vs %g", allocations, baseline)
			}
			t.Logf("retained=%d historical lookups=0 active history records=0 clone allocations=%g", count, allocations)
		})
	}
}
func TestActionProjectionMigrationResumesFrozenPartialState(t *testing.T) {
	s, m := seedArchivedAttempts(t, 500)
	at := time.Now().UTC()
	mustProjectionResult(t, s, Command{Kind: "action_project", Revision: "begin", At: at})
	revision := s.fsm.image.ActionProjectionState.Revision
	mustProjectionResult(t, s, Command{Kind: "action_project", Revision: "step", MonitorID: m.ID, Generation: revision, At: at})
	state := s.fsm.image.ActionProjectionState.clone()
	if state.Current == nil || state.Current.After == "" || state.Ready {
		t.Fatal("migration was not bounded")
	}
	if result := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "success", At: at})[0]; result.Err != errActionProjectionPending {
		t.Fatal("partial migration admitted a check")
	}
	snapshot := projectionSnapshot(t, s)
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(snapshot))); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, s.fsm.image.ActionProjectionState) {
		t.Fatal("migration cursor changed")
	}
	if err := s.migrateActionProjection(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = projectionSnapshot(t, s)
}
func TestActionProjectionRejectsForgedRecoveryCache(t *testing.T) {
	s, _ := seedArchivedAttempts(t, 500)
	if err := s.migrateActionProjection(t.Context()); err != nil {
		t.Fatal(err)
	}
	frozen, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Close()
	original := s.fsm.image.Monitors["stable-one"]
	for _, change := range []func(*ActionProjection){
		func(p *ActionProjection) { p.LastFailure = time.Time{} },
		func(p *ActionProjection) { p.RejectedAt = time.Now().UTC() },
		func(p *ActionProjection) {
			a := Action{ID: "historical-000001", Kind: "intervention"}
			p.LatestRecovery = &a
		},
	} {
		m := original.Clone()
		change(m.ActionProjection)
		i := s.fsm.image
		i.Monitors = map[string]Monitor{m.ID: m}
		worker, err := newWorkerActionValidation(i)
		if err != nil {
			t.Fatal(err)
		}
		if validateProjectionInventory(i, frozen, worker) == nil {
			t.Fatal("forged admission summary accepted")
		}
	}
}

func mustProjectionResult(t *testing.T, s *Store, c Command) Result {
	t.Helper()
	r := submit(t, s, c)[0]
	if r.Err != nil || !r.Allowed {
		t.Fatalf("migration: %+v", r)
	}
	return r
}

func TestActionProjectionPreservesExistingBlueCapacity(t *testing.T) {
	s := openCatalogMemory(t)
	m, original, at := unknownRecoveryWithBlue(t)
	opening := notificationByColor(t, m, "blue", Queued)
	condition := m.Notifications.ReviewAlerts[opening.ReviewNotice.AlertID]
	m.Actions = map[string]Action{}
	m.Sequence = 200
	m.Notifications.ReviewAlerts = map[string]ReviewAlert{}
	for n := range 65 {
		a, b, alert := original.Clone(), opening.Clone(), condition
		a = migrationUnknownAction(m, a, fmt.Sprint(n))
		alert.ID = identity(fmt.Sprintf("migration-alert-%d", n))
		alert.ActionID = a.ID
		b.ID = identity(fmt.Sprintf("migration-delivery-%d", n))
		b.DeliveryID = b.ID
		b.NotificationEpisode, b.NotificationCorrelation = alert.ID, alert.ID
		b.ReviewNotice = &ReviewNotice{AlertID: alert.ID, ActionID: a.ID, Phase: "open", Health: opening.ReviewNotice.Health}
		alert.OpeningDelivery = b.ID
		alert.Owner = nil
		m.Actions[a.ID], m.Actions[b.ID] = a, b
		m.Notifications.ReviewAlerts[alert.ID] = alert
	}
	s.fsm.mu.Lock()
	f := s.fsm
	f.image.ActionProjectionState = nil
	f.actionPosition = actionLedgerPosition{Index: f.image.Index + 1, Ordinal: 1}
	f.installMonitor(Monitor{}, m, at)
	if err := f.flushActionWrites(); err != nil {
		s.fsm.mu.Unlock()
		t.Fatal(err)
	}
	f.image.Index = f.actionPosition.Index
	s.fsm.mu.Unlock()
	submit(t, s, Command{Kind: "barrier", At: at})
	_ = projectionSnapshot(t, s)
	if err := s.migrateActionProjection(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Get(m.ID)
	if current.ActionProjection.PreviousReviewCapacity != 65 || len(current.Actions) != 65 {
		t.Fatal("existing work lost", len(current.Actions))
	}
	raw := projectionSnapshot(t, s)
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(raw))); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Get(m.ID)
	id := sortedActions(current.Actions)[0]
	a := current.Actions[id]
	mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, NoticeRevision: ActionNoticeRevision(a), At: at.Add(time.Millisecond), ExecutorSession: s.executorSession})
	if r := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: id, Outcome: "success", At: at.Add(2 * time.Millisecond)})[0]; r.Err != nil || r.Monitor == nil {
		t.Fatal("result", r.Err)
	}
	current, _ = s.Get(m.ID)
	if current.ActionProjection.PreviousReviewCapacity != 0 || len(current.Actions) != 64 || current.RecoveryHeld() != true {
		t.Fatal("transitional capacity did not drain", current.ActionProjection)
	}
	if !retainedAction(t, s, id).Deadline.Equal(a.Deadline) {
		t.Fatal("original deadline changed")
	}
	_ = projectionSnapshot(t, s)
}
func migrationUnknownAction(m Monitor, a Action, suffix string) Action {
	a.ID = identity("migration-unknown/" + suffix)
	a.CatalogUID, a.FileIncarnation = m.CatalogUID, m.FileIncarnation
	a.State = Unknown
	return a
}

func TestActionProjectionMarkerRequiresCompleteInventory(t *testing.T) {
	s, _ := seedArchivedAttempts(t, 0)
	i := s.fsm.image
	i.ActionProjectionState = &actionProjectionState{Version: 1, Revision: 1, Ready: true}
	if validateActionProjectionState(i) == nil {
		t.Fatal("ready marker omitted monitor")
	}
	m := i.Monitors["stable-one"]
	m.ActionProjection = &ActionProjection{Version: 1}
	i.Monitors = map[string]Monitor{m.ID: m}
	i.ActionProjectionState = nil
	if validateActionProjectionState(i) == nil {
		t.Fatal("projection accepted without cutover")
	}
}

func TestActionProjectionFirstRecoveryUsesCollectionOnlyDiskLedger(t *testing.T) {
	s := openCatalogMemory(t)
	cfg := testMonitor()
	cfg.Policy.Intervention = true
	cfg.Policy.Endpoints = nil
	submit(t, s, Command{Kind: "configure", MonitorID: cfg.ID, Revision: cfg.Revision, Config: &cfg, At: time.Now().UTC()})
	ledger := newLedgerTest(t, true, 8<<20)
	s.fsm.mu.Lock()
	old := s.fsm.collections
	s.fsm.collections = ledger
	s.fsm.image.ActionStorage = &actionStorageState{}
	s.fsm.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	// The pending first action must not require a nonexistent historical index.
	m, _ := s.Get("stable-one")
	r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: time.Now().UTC()})[0]
	if r.Err != nil || r.Monitor == nil {
		t.Fatal(r.Err)
	}
	if !r.Monitor.pendingIntervention() {
		t.Fatal("first recovery not admitted")
	}
	_ = projectionSnapshot(t, s)
}

func TestActionProjectionFirstBluePolicyUsesCollectionOnlyDiskLedger(t *testing.T) {
	s := openCatalogMemory(t)
	ledger := newLedgerTest(t, true, 8<<20)
	s.fsm.mu.Lock()
	old := s.fsm.collections
	s.fsm.collections = ledger
	s.fsm.image.ActionStorage = &actionStorageState{}
	s.fsm.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	m := testMonitor()
	m.Policy.Endpoints = map[string]int{"blue": 1}
	mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now().UTC()})
	current, _ := s.Get(m.ID)
	if current.ReviewPolicyID == "" {
		t.Fatal("initial blue policy was not captured")
	}
	_ = projectionSnapshot(t, s)
}

func TestActionProjectionAdoptsOriginalFileIdentityWithoutRebindingEvidence(t *testing.T) {
	s := openCatalogMemory(t)
	// Replay the oldest episode format supported by this migration. No file
	// incarnation was assigned to monitors written before format 42.
	s.fsm.mu.Lock()
	s.fsm.image.ActionProjectionState = nil
	s.fsm.mu.Unlock()
	m := testMonitor()
	m.Policy.Intervention = true
	m.Policy.Endpoints = nil
	at := time.Now().UTC()
	applyCatalogFormat(t, s, DeliveryFormatVersion, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	r := applyCatalogFormat(t, s, DeliveryFormatVersion, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at})[0]
	if r.Err != nil || r.Monitor == nil {
		t.Fatal("historical outage", r.Err)
	}
	m = *r.Monitor
	a := interventionAction(t, m)
	applyCatalogFormat(t, s, DeliveryFormatVersion, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at})
	applyCatalogFormat(t, s, DeliveryFormatVersion, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Outcome: "failure", Ambiguous: true, At: at.Add(time.Millisecond)})
	applyCatalogFormat(t, s, ActionFormatVersion, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 2, Outcome: "failure", At: at.Add(2 * time.Millisecond)})
	if err := s.migrateActionProjection(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(m.ID)
	original := retainedAction(t, s, a.ID)
	if m.FileIncarnation != "" || original.FileIncarnation != "" || !m.RecoveryHeld() {
		t.Fatal("historical original file identity changed")
	}
	catalog := catalogRecord(t, s, "Monitor", m.ID, "adopted-original-file", "catalog-r1", "sealed fixture")
	createCatalog(t, s, catalog)
	guard := CatalogGuard{Conditions: catalogConditions(t, s, []CatalogKey{catalog.Key})}
	configured := m.Clone()
	configured.CatalogUID = catalog.UID
	result := mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &configured, Guard: &guard, At: at.Add(3 * time.Millisecond)})
	adopted := result.Monitor
	if adopted.AdoptedFileIncarnation == nil || *adopted.AdoptedFileIncarnation != "" || !adopted.RecoveryHeld() || !adopted.OwnsAction(original) {
		t.Fatal("original hold was orphaned")
	}
	if !reflect.DeepEqual(original, retainedAction(t, s, a.ID)) {
		t.Fatal("adoption rewrote provider evidence")
	}
	raw := projectionSnapshot(t, s)
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(raw))); err != nil {
		t.Fatal(err)
	}
	adoptedAfter, _ := s.Get(m.ID)
	if !adoptedAfter.OwnsAction(original) || !adoptedAfter.RecoveryHeld() {
		t.Fatal("snapshot lost original-file hold")
	}
}
