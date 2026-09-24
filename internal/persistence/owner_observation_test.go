package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func assertObserved(t *testing.T, s *Store, id string, want uint64) {
	t.Helper()
	status, ok, err := s.MonitorStatus(id)
	if err != nil || !ok || status.ObservedGeneration != want {
		t.Fatalf("observedGeneration want %d, got %+v exists=%v error=%v", want, status, ok, err)
	}
}
func TestOwnerObservationRequiresInstallAndCurrentClosure(t *testing.T) {
	s := openCatalogMemory(t)
	m, record, secret, g := prepareGuardFixture(t, s)
	configured := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: g, At: time.Now().UTC()})[0]
	if configured.Err != nil {
		t.Fatal(configured.Err)
	}
	assertObserved(t, s, m.ID, 0)
	if err := s.MarkMonitorObserved(m.ID, *g, record.Generation+1); !errors.Is(err, ErrCatalogDependency) {
		t.Fatal("wrong config generation marked", err)
	}
	partial := g.Clone()
	partial.Conditions = partial.Conditions[:1]
	if err := s.MarkMonitorObserved(m.ID, partial, record.Generation); !errors.Is(err, ErrCatalogDependency) {
		t.Fatal("partial closure marked", err)
	}
	if err := s.MarkMonitorObserved(m.ID, *g, record.Generation); err != nil {
		t.Fatal(err)
	}
	assertObserved(t, s, m.ID, record.Generation)
	// Pulse generation is independent and does not consume the owner marker.
	m = *configured.Monitor
	m = controlPulse(t, s, m, g, time.Now().UTC(), "success")
	assertObserved(t, s, m.ID, record.Generation)
	createCatalog(t, s, catalogRecord(t, s, "Credential", "unrelated", "unrelated-uid", "unrelated-v1", "not used"))
	assertObserved(t, s, m.ID, record.Generation)
	s.fsm.mu.RLock()
	cached := s.fsm.image.Monitors[m.ID].ownerObserved.catalogSequence
	sequence := s.fsm.catalogSequence
	s.fsm.mu.RUnlock()
	if cached != sequence {
		t.Fatal("successful revalidation not cached")
	}
	secret = requireCatalog(t, s, secret.Key)
	mutation := updateCatalogMutation(t, s, secret, "secret-v2", "rotated")
	if result := catalogSubmit(t, s, mutation); result.Err != nil {
		t.Fatal(result.Err)
	}
	assertObserved(t, s, m.ID, 0)
	if err := s.MarkMonitorObserved(m.ID, *g, record.Generation); !errors.Is(err, ErrCatalogDependency) {
		t.Fatal("old prepared dependency marked", err)
	}
	current := g.Clone()
	current.Conditions = catalogConditions(t, s, []CatalogKey{record.Key, g.Conditions[1].Key, secret.Key})
	// Refreshing the identity guard alone cannot acknowledge old executable jobs.
	if err := s.MarkMonitorObserved(m.ID, current, record.Generation); !errors.Is(err, ErrCatalogDependency) {
		t.Fatal("uninstalled dependency marked", err)
	}
	result := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: &current, At: time.Now().UTC()})[0]
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	assertObserved(t, s, m.ID, 0)
	if err := s.MarkMonitorObserved(m.ID, current, record.Generation); err != nil {
		t.Fatal(err)
	}
	assertObserved(t, s, m.ID, record.Generation)
}
func TestOwnerObservationIsNotSerializedOrReplayed(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := managedControlMonitor(t, s, "restart")
	if err := s.MarkMonitorObserved(m.ID, *g, 1); err != nil {
		t.Fatal(err)
	}
	assertObserved(t, s, m.ID, 1)
	s.fsm.mu.RLock()
	raw, err := json.Marshal(s.fsm.image)
	s.fsm.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("ownerObserved")) || bytes.Contains(raw, []byte("observed_generation")) {
		t.Fatal("process-local observation entered snapshot")
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
	assertObserved(t, s, m.ID, 0)
	if err := s.MarkMonitorObserved(m.ID, *g, 1); err != nil {
		t.Fatal(err)
	}
	assertObserved(t, s, m.ID, 1)
}
func TestOwnerObservationConfigEditInvalidatesBeforeAndAfterConfigure(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := managedControlMonitor(t, s, "edited")
	if err := s.MarkMonitorObserved(m.ID, *g, 1); err != nil {
		t.Fatal(err)
	}
	record := requireCatalog(t, s, CatalogKey{Kind: "Monitor", ID: m.ID})
	mutation := updateCatalogMutation(t, s, record, "new-resource-version", "changed configuration")
	if result := catalogSubmit(t, s, mutation); result.Err != nil {
		t.Fatal(result.Err)
	}
	assertObserved(t, s, m.ID, 0)
	next := CatalogGuard{Conditions: catalogConditions(t, s, []CatalogKey{record.Key})}
	result := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: &next, At: time.Now().UTC()})[0]
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	assertObserved(t, s, m.ID, 0)
	if err := s.MarkMonitorObserved(m.ID, next, mutation.Record.Generation); err != nil {
		t.Fatal(err)
	}
	assertObserved(t, s, m.ID, mutation.Record.Generation)
}
