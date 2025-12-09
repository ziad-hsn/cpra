package entities_test

import (
	"cpra/internal/controller/entities"
	"cpra/internal/loader/schema"
	"testing"
	"time"

	"github.com/mlange-42/ark-tools/app"
)

// BenchmarkCreateEntity measures the allocations during entity creation.
// It compares the baseline (current state) with the optimized version.
func BenchmarkCreateEntity(b *testing.B) {
	arkApp := app.New(1024)
	world := &arkApp.World
	mapper := entities.NewEntityManager(world)

	// Pre-allocate a monitor to avoid benchmarking schema creation
	monitor := schema.Monitor{
		Name:    "benchmark-monitor",
		Enabled: true,
		Pulse: schema.Pulse{
			Type:     "http",
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
			Config: &schema.PulseHTTPConfig{
				Url:    "http://example.com",
				Method: "GET",
			},
		},
		Codes: map[string]schema.CodeConfig{
			"Red": {
				Notify: "log",
				Config: &schema.CodeNotificationLog{File: "stdout"},
			},
			"Green": {
				Notify: "log",
				Config: &schema.CodeNotificationLog{File: "stdout"},
			},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// We create a new world/mapper every N iterations to avoid unbounded growth?
		// Actually, for alloc benchmark, we just want to measure CreateEntity.
		// But Ark world growth might affect it. Let's just create.
		err := mapper.CreateEntityFromMonitor(&monitor, world)
		if err != nil {
			b.Fatalf("Failed to create entity: %v", err)
		}
		
		// Cleanup to keep memory usage stable if running long
		// world.Reset() // Ark doesn't expose easy reset without re-init
	}
}

// BenchmarkCreateEntitiesBatch measures batch creation allocations.
func BenchmarkCreateEntitiesBatch(b *testing.B) {
	arkApp := app.New(1024)
	world := &arkApp.World
	mapper := entities.NewEntityManager(world)

	monitors := make([]schema.Monitor, 100)
	for i := 0; i < 100; i++ {
		monitors[i] = schema.Monitor{
			Name:    "benchmark-monitor",
			Enabled: true,
			Pulse: schema.Pulse{
				Type:     "http",
				Interval: 10 * time.Second,
				Timeout:  5 * time.Second,
				Config: &schema.PulseHTTPConfig{
					Url:    "http://example.com",
					Method: "GET",
				},
			},
			Codes: map[string]schema.CodeConfig{
				"Red": {
					Notify: "log",
					Config: &schema.CodeNotificationLog{File: "stdout"},
				},
			},
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		err := mapper.CreateEntitiesFromMonitors(world, monitors)
		if err != nil {
			b.Fatalf("Failed to create entities: %v", err)
		}
	}
}
