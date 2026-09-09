package systems

import (
	"testing"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/loader/schema"
	"cpra/internal/web/snapshot"

	"github.com/mlange-42/ark/ecs"
)

// noopLogger satisfies the systems.Logger interface without output.
type noopLogger struct{}

func (noopLogger) Info(string, ...interface{})                     {}
func (noopLogger) Debug(string, ...interface{})                    {}
func (noopLogger) Warn(string, ...interface{})                     {}
func (noopLogger) Error(string, ...interface{})                    {}
func (noopLogger) LogSystemPerformance(string, time.Duration, int) {}
func (noopLogger) LogComponentState(uint32, string, string)        {}

func mkMonitor(name, ptype string, enabled bool) *schema.Monitor {
	cfg := &schema.PulseHTTPConfig{Url: "http://example.com/health"}
	if ptype == "tcp" {
		cfg = nil
	}
	m := &schema.Monitor{
		Name:    name,
		Enabled: enabled,
		Pulse: schema.Pulse{
			Type:     ptype,
			Interval: time.Second,
			Timeout:  time.Second,
		},
	}
	if ptype == "http" {
		m.Pulse.Config = &schema.PulseHTTPConfig{Url: "http://example.com/health"}
	} else if ptype == "tcp" {
		m.Pulse.Config = &schema.PulseTCPConfig{Host: "example.com", Port: 80}
	}
	_ = cfg
	return m
}

// setState finds a monitor entity by name and mutates its state for the test.
func setState(t *testing.T, w *ecs.World, mgr *entities.EntityManager, name string, mut func(*components.MonitorState)) {
	t.Helper()
	q := ecs.NewFilter1[components.MonitorState](w).Query()
	for q.Next() {
		st := q.Get()
		if st.Name == name {
			mut(st)
			return
		}
	}
	t.Fatalf("monitor %q not found", name)
}

func TestStatsSnapshotAggregates(t *testing.T) {
	w := ecs.NewWorld()
	mgr := entities.NewEntityManager(&w)

	if err := mgr.CreateEntityFromMonitor(mkMonitor("up-api", "http", true), &w); err != nil {
		t.Fatalf("create up-api: %v", err)
	}
	if err := mgr.CreateEntityFromMonitor(mkMonitor("down-db", "tcp", true), &w); err != nil {
		t.Fatalf("create down-db: %v", err)
	}
	if err := mgr.CreateEntityFromMonitor(mkMonitor("off-maint", "http", false), &w); err != nil {
		t.Fatalf("create off-maint: %v", err)
	}

	// Force "down-db" into a down state via failures.
	setState(t, &w, mgr, "down-db", func(st *components.MonitorState) {
		st.PulseFailures = 3
	})
	// Force "up-api" into an incident to test the incident bucket too.
	setState(t, &w, mgr, "up-api", func(st *components.MonitorState) {
		st.Flags |= components.StateIncidentOpen
		st.PendingCode = "red"
	})

	holder := snapshot.NewHolder()
	sys := NewBatchStatsSnapshotSystem(&w, noopLogger{}, holder, time.Second, 50000)
	sys.Initialize(&w)
	// Bypass throttle by calling buildSnapshot directly.
	sys.buildSnapshot(time.Now())

	snap := holder.Get()
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.Total != 3 {
		t.Fatalf("total = %d, want 3", snap.Total)
	}
	if snap.Disabled != 1 {
		t.Fatalf("disabled = %d, want 1", snap.Disabled)
	}
	if snap.ByStatus["incident"] != 1 {
		t.Fatalf("incident = %d, want 1 (got %v)", snap.ByStatus["incident"], snap.ByStatus)
	}
	if snap.ByStatus["down"] != 1 {
		t.Fatalf("down = %d, want 1 (got %v)", snap.ByStatus["down"], snap.ByStatus)
	}
	if snap.ByPulseType["http"] != 1 || snap.ByPulseType["tcp"] != 1 {
		t.Fatalf("by_pulse_type = %v", snap.ByPulseType)
	}
	if snap.ByCode["red"] != 1 {
		t.Fatalf("by_code = %v, want red=1", snap.ByCode)
	}
	if len(snap.Monitors) != 2 { // disabled excluded from active index
		t.Fatalf("monitors index len = %d, want 2 (disabled excluded)", len(snap.Monitors))
	}
	if snap.ByID == nil || len(snap.ByID) != 2 {
		t.Fatalf("ByID = %v, want 2 entries", snap.ByID)
	}
}

func TestStatsSnapshotThrottle(t *testing.T) {
	w := ecs.NewWorld()
	holder := snapshot.NewHolder()
	sys := NewBatchStatsSnapshotSystem(&w, noopLogger{}, holder, time.Hour, 50000)
	sys.Initialize(&w)
	sys.last = time.Now() // recent
	sys.Update(&w)        // should be throttled -> no snapshot
	if holder.Get() != nil {
		t.Fatal("expected nil snapshot when throttled")
	}
}
