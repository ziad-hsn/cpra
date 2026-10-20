package systems

import (
	"fmt"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestFileStartupBoundsConfigureAndRemovalBatches(t *testing.T) {
	for _, batchSize := range []int{3, 1000} {
		t.Run(fmt.Sprint(batchSize), func(t *testing.T) {
			cfg := runtimeconfig.Default()
			cfg.Storage.Mode, cfg.Storage.BatchSize = "memory", batchSize
			store, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			world := ecs.NewWorld()
			manager := entities.NewEntityManager(&world)
			count := 8
			if batchSize == 1000 {
				count = 1001 // Cross both the count and conservative encoded-size limits.
			}
			for n := range count {
				monitor := manifest.Monitor{ID: fmt.Sprintf("file-%04d", n), Name: fmt.Sprintf("File monitor %d", n), Enabled: true,
					Pulse: manifest.Pulse{Type: "http", Interval: time.Minute, Timeout: 10 * time.Second, UnhealthyThreshold: 2, HealthyThreshold: 2,
						Config: &manifest.PulseHTTPConfig{Url: "http://127.0.0.1:1"}}}
				if err := manager.CreateEntityFromMonitor(&monitor, &world); err != nil {
					t.Fatal(err)
				}
			}
			load := func(w *ecs.World) {
				t.Helper()
				system := NewDurableSystem(w, store, cfg, fleetview.NewHolder(), noopLogger{}, nil, nil, nil, nil, time.Minute, false)
				if err := system.Load(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			load(&world)
			before, ok := store.Get("file-0000")
			if !ok || before.FileIncarnation == "" {
				t.Fatal("file monitor was not committed")
			}
			load(&world) // The same configured identities survive repeated initialization.
			for n := range count {
				m, ok := store.Get(fmt.Sprintf("file-%04d", n))
				if !ok || m.Removed || m.TotalChecks != 0 || len(m.Actions) != 0 {
					t.Fatal("startup omitted a monitor or executed work", n)
				}
			}
			after, _ := store.Get(before.ID)
			if after.FileIncarnation != before.FileIncarnation {
				t.Fatal("unchanged startup replaced monitor identity")
			}
			empty := ecs.NewWorld()
			load(&empty)
			for n := range count {
				m, ok := store.Get(fmt.Sprintf("file-%04d", n))
				if !ok || !m.Removed {
					t.Fatal("reconciliation omitted removed monitor", n)
				}
			}
			if !store.Status().Ready {
				t.Fatal("bounded startup or removal made storage unavailable")
			}
		})
	}
}
