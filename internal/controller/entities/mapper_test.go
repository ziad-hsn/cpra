package entities

import (
	"sync"
	"testing"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/loader/schema"

	"github.com/mlange-42/ark-tools/app"
	"github.com/mlange-42/ark/ecs"
)

// Test helpers

func newTestWorld() *ecs.World {
	a := app.New(64)
	return &a.World
}

func newTestMonitor(name string) *schema.Monitor {
	return &schema.Monitor{
		Name:    name,
		Enabled: true,
		Pulse: schema.Pulse{
			Type:     "http",
			Interval: 30 * time.Second,
			Timeout:  5 * time.Second,
			Config:   &schema.PulseHTTPConfig{Url: "http://localhost"},
		},
	}
}

// Pool function tests

func TestGetPutMonitorState(t *testing.T) {
	t.Parallel()

	t.Run("GetReturnsValidPointer", func(t *testing.T) {
		m := GetMonitorState()
		if m == nil {
			t.Fatal("GetMonitorState returned nil")
		}
	})

	t.Run("PutHandlesNil", func(t *testing.T) {
		// Should not panic
		PutMonitorState(nil)
	})

	t.Run("PutResetsState", func(t *testing.T) {
		m := GetMonitorState()
		m.Name = "test"
		m.ConsecutiveFailures = 5
		PutMonitorState(m)

		// Get again - may or may not be the same instance
		m2 := GetMonitorState()
		if m2.Name != "" {
			t.Error("MonitorState.Name should be reset")
		}
		if m2.ConsecutiveFailures != 0 {
			t.Error("MonitorState.ConsecutiveFailures should be reset")
		}
		PutMonitorState(m2)
	})
}

func TestGetPutPulseConfig(t *testing.T) {
	t.Parallel()

	t.Run("GetReturnsValidPointer", func(t *testing.T) {
		p := GetPulseConfig()
		if p == nil {
			t.Fatal("GetPulseConfig returned nil")
		}
		PutPulseConfig(p)
	})

	t.Run("PutHandlesNil", func(t *testing.T) {
		PutPulseConfig(nil)
	})

	t.Run("PutResetsConfig", func(t *testing.T) {
		p := GetPulseConfig()
		p.Type = "http"
		p.Interval = 30 * time.Second
		PutPulseConfig(p)

		p2 := GetPulseConfig()
		if p2.Type != "" {
			t.Error("PulseConfig.Type should be reset")
		}
		if p2.Interval != 0 {
			t.Error("PulseConfig.Interval should be reset")
		}
		PutPulseConfig(p2)
	})
}

func TestGetPutInterventionConfig(t *testing.T) {
	t.Parallel()

	t.Run("GetReturnsValidPointer", func(t *testing.T) {
		i := GetInterventionConfig()
		if i == nil {
			t.Fatal("GetInterventionConfig returned nil")
		}
		PutInterventionConfig(i)
	})

	t.Run("PutHandlesNil", func(t *testing.T) {
		PutInterventionConfig(nil)
	})

	t.Run("PutResetsConfig", func(t *testing.T) {
		i := GetInterventionConfig()
		i.Action = "restart"
		i.MaxFailures = 3
		PutInterventionConfig(i)

		i2 := GetInterventionConfig()
		if i2.Action != "" {
			t.Error("InterventionConfig.Action should be reset")
		}
		if i2.MaxFailures != 0 {
			t.Error("InterventionConfig.MaxFailures should be reset")
		}
		PutInterventionConfig(i2)
	})
}

func TestGetPutCodeConfig(t *testing.T) {
	t.Parallel()

	t.Run("GetReturnsValidPointer", func(t *testing.T) {
		c := GetCodeConfig(4)
		if c == nil {
			t.Fatal("GetCodeConfig returned nil")
		}
		PutCodeConfig(c)
	})

	t.Run("PutHandlesNil", func(t *testing.T) {
		PutCodeConfig(nil)
	})

	t.Run("GetReturnsZeroedArray", func(t *testing.T) {
		c := GetCodeConfig(4)
		for i := range c.Configs {
			if c.Configs[i] != 0 {
				t.Errorf("CodeConfig.Configs[%d] should be 0", i)
			}
		}
		PutCodeConfig(c)
	})
}

func TestGetPutCodeStatus(t *testing.T) {
	t.Parallel()

	t.Run("GetReturnsValidPointer", func(t *testing.T) {
		s := GetCodeStatus(4)
		if s == nil {
			t.Fatal("GetCodeStatus returned nil")
		}
		PutCodeStatus(s)
	})

	t.Run("PutHandlesNil", func(t *testing.T) {
		PutCodeStatus(nil)
	})

	t.Run("GetReturnsZeroedArray", func(t *testing.T) {
		s := GetCodeStatus(4)
		for i := range s.Status {
			if s.Status[i].ConsecutiveFailures != 0 {
				t.Errorf("CodeStatus.Status[%d].ConsecutiveFailures should be 0", i)
			}
		}
		PutCodeStatus(s)
	})
}

func TestGetPutJobStorage(t *testing.T) {
	t.Parallel()

	t.Run("GetReturnsValidPointer", func(t *testing.T) {
		j := GetJobStorage()
		if j == nil {
			t.Fatal("GetJobStorage returned nil")
		}
		PutJobStorage(j)
	})

	t.Run("PutHandlesNil", func(t *testing.T) {
		PutJobStorage(nil)
	})

	t.Run("GetReturnsEmptyStorage", func(t *testing.T) {
		j := GetJobStorage()
		if j.PulseJob != nil {
			t.Error("JobStorage.PulseJob should be nil")
		}
		if j.InterventionJob != nil {
			t.Error("JobStorage.InterventionJob should be nil")
		}
		PutJobStorage(j)
	})
}

func TestGetPutColorCodeStatus(t *testing.T) {
	t.Parallel()

	t.Run("GetReturnsValidPointer", func(t *testing.T) {
		c := GetColorCodeStatus()
		if c == nil {
			t.Fatal("GetColorCodeStatus returned nil")
		}
	})

	t.Run("PutIsNoOp", func(t *testing.T) {
		c := GetColorCodeStatus()
		// Should not panic - this is a no-op
		PutColorCodeStatus(c)
		PutColorCodeStatus(nil)
	})
}

func TestPoolConcurrency(t *testing.T) {
	t.Parallel()

	const goroutines = 10
	const iterations = 100

	var wg sync.WaitGroup

	// Test MonitorState pool concurrently
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				m := GetMonitorState()
				m.Name = "test"
				PutMonitorState(m)
			}
		}()
	}

	// Test PulseConfig pool concurrently
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				p := GetPulseConfig()
				p.Type = "http"
				PutPulseConfig(p)
			}
		}()
	}

	wg.Wait()
}

// EntityManager tests

func TestNewEntityManager(t *testing.T) {
	t.Parallel()

	world := newTestWorld()
	em := NewEntityManager(world)

	if em == nil {
		t.Fatal("NewEntityManager returned nil")
	}

	// Check all mappers are initialized
	if em.MonitorState == nil {
		t.Error("MonitorState mapper not initialized")
	}
	if em.PulseConfig == nil {
		t.Error("PulseConfig mapper not initialized")
	}
	if em.InterventionConfig == nil {
		t.Error("InterventionConfig mapper not initialized")
	}
	if em.CodeConfig == nil {
		t.Error("CodeConfig mapper not initialized")
	}
	if em.CodeStatus == nil {
		t.Error("CodeStatus mapper not initialized")
	}
	if em.JobStorage == nil {
		t.Error("JobStorage mapper not initialized")
	}
	if em.Shard == nil {
		t.Error("Shard mapper not initialized")
	}
	if em.Disabled == nil {
		t.Error("Disabled mapper not initialized")
	}

	// Check default shard slots
	if em.shardSlots != components.DefaultShardSlots {
		t.Errorf("shardSlots = %d, want %d", em.shardSlots, components.DefaultShardSlots)
	}
}

func TestSetShardSlots(t *testing.T) {
	t.Parallel()

	world := newTestWorld()
	em := NewEntityManager(world)

	t.Run("PositiveValue", func(t *testing.T) {
		em.SetShardSlots(50)
		if em.shardSlots != 50 {
			t.Errorf("shardSlots = %d, want 50", em.shardSlots)
		}
	})

	t.Run("ZeroFallsBackToDefault", func(t *testing.T) {
		em.SetShardSlots(0)
		if em.shardSlots != components.DefaultShardSlots {
			t.Errorf("shardSlots = %d, want %d", em.shardSlots, components.DefaultShardSlots)
		}
	})

	t.Run("NegativeFallsBackToDefault", func(t *testing.T) {
		em.SetShardSlots(-10)
		if em.shardSlots != components.DefaultShardSlots {
			t.Errorf("shardSlots = %d, want %d", em.shardSlots, components.DefaultShardSlots)
		}
	})
}

func TestCreateEntityFromMonitor_Errors(t *testing.T) {
	t.Parallel()

	t.Run("NilWorld", func(t *testing.T) {
		world := newTestWorld()
		em := NewEntityManager(world)
		monitor := newTestMonitor("test")

		err := em.CreateEntityFromMonitor(monitor, nil)
		if err == nil {
			t.Error("expected error for nil world")
		}
	})

	t.Run("NilManager", func(t *testing.T) {
		world := newTestWorld()
		monitor := newTestMonitor("test")

		var em *EntityManager
		err := em.CreateEntityFromMonitor(monitor, world)
		if err == nil {
			t.Error("expected error for nil manager")
		}
	})

	t.Run("EmptyName", func(t *testing.T) {
		world := newTestWorld()
		em := NewEntityManager(world)
		monitor := newTestMonitor("")

		err := em.CreateEntityFromMonitor(monitor, world)
		if err == nil {
			t.Error("expected error for empty name")
		}
	})
}

func TestCreateEntityFromMonitor_ValidMonitor(t *testing.T) {
	t.Parallel()

	world := newTestWorld()
	em := NewEntityManager(world)
	monitor := newTestMonitor("test-monitor")

	err := em.CreateEntityFromMonitor(monitor, world)
	if err != nil {
		t.Fatalf("CreateEntityFromMonitor failed: %v", err)
	}
}

func TestCreateEntityFromMonitor_DisabledMonitor(t *testing.T) {
	t.Parallel()

	world := newTestWorld()
	em := NewEntityManager(world)
	monitor := newTestMonitor("disabled-monitor")
	monitor.Enabled = false

	err := em.CreateEntityFromMonitor(monitor, world)
	if err != nil {
		t.Fatalf("CreateEntityFromMonitor failed: %v", err)
	}
}

func TestCreateEntityFromMonitor_WithIntervention(t *testing.T) {
	t.Parallel()

	world := newTestWorld()
	em := NewEntityManager(world)
	monitor := newTestMonitor("intervention-monitor")
	monitor.Intervention = schema.Intervention{
		Action:  "docker",
		Retries: 3,
		Target: &schema.InterventionTargetDocker{
			Type:      "restart",
			Container: "my-container",
		},
	}

	err := em.CreateEntityFromMonitor(monitor, world)
	if err != nil {
		t.Fatalf("CreateEntityFromMonitor failed: %v", err)
	}
}

func TestCreateEntityFromMonitor_WithCodes(t *testing.T) {
	t.Parallel()

	world := newTestWorld()
	em := NewEntityManager(world)
	monitor := newTestMonitor("code-monitor")
	monitor.Codes = schema.Codes{
		"red": {
			Notify:   "log",
			Dispatch: true,
			Config:   &schema.CodeNotificationLog{File: "/var/log/alerts.log"},
		},
		"green": {
			Notify:   "log",
			Dispatch: false,
			Config:   &schema.CodeNotificationLog{File: "/var/log/recovery.log"},
		},
	}

	err := em.CreateEntityFromMonitor(monitor, world)
	if err != nil {
		t.Fatalf("CreateEntityFromMonitor failed: %v", err)
	}
}

func TestCreateEntitiesFromMonitors_Empty(t *testing.T) {
	t.Parallel()

	world := newTestWorld()
	em := NewEntityManager(world)

	err := em.CreateEntitiesFromMonitors(world, nil)
	if err != nil {
		t.Errorf("expected nil error for empty monitors, got %v", err)
	}

	err = em.CreateEntitiesFromMonitors(world, []schema.Monitor{})
	if err != nil {
		t.Errorf("expected nil error for empty slice, got %v", err)
	}
}

func TestCreateEntitiesFromMonitors_Batch(t *testing.T) {
	t.Parallel()

	world := newTestWorld()
	em := NewEntityManager(world)

	monitors := []schema.Monitor{
		*newTestMonitor("monitor-1"),
		*newTestMonitor("monitor-2"),
		*newTestMonitor("monitor-3"),
	}

	err := em.CreateEntitiesFromMonitors(world, monitors)
	if err != nil {
		t.Fatalf("CreateEntitiesFromMonitors failed: %v", err)
	}
}

func TestCreateEntitiesFromMonitors_NilWorld(t *testing.T) {
	t.Parallel()

	world := newTestWorld()
	em := NewEntityManager(world)

	monitors := []schema.Monitor{
		*newTestMonitor("monitor-1"),
	}

	err := em.CreateEntitiesFromMonitors(nil, monitors)
	if err == nil {
		t.Error("expected error for nil world")
	}
}

func TestCreateEntitiesFromMonitors_NilManager(t *testing.T) {
	t.Parallel()

	world := newTestWorld()

	monitors := []schema.Monitor{
		*newTestMonitor("monitor-1"),
	}

	var em *EntityManager
	err := em.CreateEntitiesFromMonitors(world, monitors)
	if err == nil {
		t.Error("expected error for nil manager")
	}
}
