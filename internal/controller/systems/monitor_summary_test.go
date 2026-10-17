package systems

import (
	"errors"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"

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

func TestCommittedProjectionAggregates(t *testing.T) {
	world := ecs.NewWorld()
	manager := entities.NewEntityManager(&world)
	for _, spec := range []struct {
		name, driver string
		enabled      bool
	}{{"up-api", "http", true}, {"down-db", "tcp", true}, {"off-maint", "http", false}} {
		monitor := manifest.Monitor{Name: spec.name, Enabled: spec.enabled, Pulse: manifest.Pulse{Type: spec.driver, Interval: time.Second, Timeout: time.Second, UnhealthyThreshold: 1, HealthyThreshold: 1}}
		if spec.name == "down-db" {
			monitor.Codes = manifest.Codes{"red": {Dispatch: true, Notify: "log", Config: &manifest.CodeNotificationLog{File: "notification.jsonl"}}}
		}
		if spec.driver == "http" {
			monitor.Pulse.Config = &manifest.PulseHTTPConfig{Url: "http://example.com/health"}
		} else {
			monitor.Pulse.Config = &manifest.PulseTCPConfig{Host: "example.com", Port: 80}
		}
		if err := manager.CreateEntityFromMonitor(&monitor, &world); err != nil {
			t.Fatal(err)
		}
	}
	system := newIncidentSystem(t, &world, nil, nil)
	entitiesByName := map[string]ecs.Entity{}
	q := ecs.NewFilter1[components.MonitorState](&world).Query()
	for q.Next() {
		entitiesByName[q.Get().Name] = q.Entity()
	}
	before := system.index.Overview()
	if before.Total != 3 || before.Disabled != 1 || before.ByStatus["unknown"] != 2 {
		t.Fatalf("initial counts: %+v", before)
	}
	applyIncidentPulse(t, system, entitiesByName["up-api"], jobs.Result{})
	applyIncidentPulse(t, system, entitiesByName["down-db"], jobs.Result{Err: errors.New("unhealthy")})
	after := system.index.Overview()
	if after.Total != 3 || after.Disabled != 1 || after.ByStatus["up"] != 1 || after.ByStatus["incident"] != 1 || after.ByStatus["unknown"] != 0 {
		t.Fatalf("committed counts: %+v", after)
	}
	if after.ByPulseType["http"] != 2 || after.ByPulseType["tcp"] != 1 {
		t.Fatal("disabled monitor missing from type counts", after.ByPulseType)
	}
	if after.ByCode["red"] != 1 {
		t.Fatal("pending notification missing from aggregate", after.ByCode)
	}
	// Reads do not require a periodic full-fleet rebuild or alter earlier views.
	if before.ByStatus["unknown"] != 2 || !after.Generated.Equal(system.index.Overview().Generated) {
		t.Fatal("observation mutated a previous view or rebuilt the fleet")
	}
	rows, total := system.index.Page(0, 2, nil)
	if total != 3 || len(rows) != 2 || rows[0].Name != "down-db" || rows[1].Name != "off-maint" || rows[1].Status != "disabled" {
		t.Fatal("bounded, ordered page lost monitor state", rows, total)
	}
	applyIncidentPulse(t, system, entitiesByName["down-db"], jobs.Result{})
	recovered := system.index.Overview()
	if recovered.ByStatus["up"] != 2 || recovered.ByStatus["incident"] != 0 || recovered.Disabled != 1 {
		t.Fatalf("recovery was not projected immediately: %+v", recovered)
	}
	if recovered.ByCode["red"] != 0 || after.ByCode["red"] != 1 {
		t.Fatal("notification cancellation was not projected or mutated the earlier view")
	}
}
