package main

import (
	"cpra/internal/controller"
	"cpra/internal/controller/entities"
	"cpra/internal/controller/systems"
	"cpra/internal/loader/loader"
	"cpra/internal/queue"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/mlange-42/ark-tools/app"
)

func main() {
	// Check for debug mode
	debugMode := os.Getenv("CPRA_DEBUG") == "true" ||
		(len(os.Args) > 1 && (os.Args[1] == "--debug" || os.Args[1] == "-d"))

	// Initialize logging
	controller.InitializeLoggers(debugMode)
	defer controller.CloseLoggers()

	controller.SystemLogger.Info("Starting CPRa Monitoring System with Optimized Queue")

	// Set optimal GC and memory settings for high throughput
	debug.SetGCPercent(50)               // More aggressive GC for queue-heavy workload
	runtime.GOMAXPROCS(runtime.NumCPU()) // Use all available cores

	// Memory manager with 2GB limit for 10k+ monitors
	memManager := controller.NewMemoryManager(2, 30)
	memManager.SetMemoryLimit()

	// Load configuration
	configFile := "internal/loader/replicated_test.yaml"
	l := loader.NewLoader("yaml", configFile)
	if err := l.Load(); err != nil {
		controller.SystemLogger.Fatal("Failed to load configuration: %v", err)
	}
	manifest := l.GetManifest()

	monitorCount := len(manifest.Monitors)
	if monitorCount == 0 {
		controller.SystemLogger.Fatal("No monitors configured")
	}

	controller.SystemLogger.Info("Initializing optimized queue for %d monitors", monitorCount)

	// Create the optimized queue manager
	queueManager, err := queue.NewQueueManager(monitorCount)
	if err != nil {
		controller.SystemLogger.Fatal("Failed to create queue manager: %v", err)
	}
	defer queueManager.Shutdown()

	// Get result channels from queue manager
	pulseResults, interventionResults, codeResults := queueManager.GetChannels()

	// Log queue configuration
	metrics := queueManager.GetMetrics()
	controller.SystemLogger.Info("Queue initialized with metrics: %+v", metrics)

	// Create ECS world and systems
	tool := app.New(1024).Seed(123)
	tool.TPS = 10000 // High TPS for 10k monitors with 1s intervals

	_, err = controller.NewCPRaWorld(&manifest, &tool.World)
	if err != nil {
		controller.SystemLogger.Fatal("Failed to create world: %v", err)
	}

	mapper := entities.InitializeMappers(&tool.World)

	// Add scheduling system (unchanged)
	tool.AddSystem(&systems.PulseScheduleSystem{
		Mapper: mapper,
	})

	// Add dispatch systems with queue manager
	tool.AddSystem(&systems.PulseDispatchSystem{
		QueueManager: queueManager,
		Mapper:       mapper,
	})

	tool.AddSystem(&systems.InterventionDispatchSystem{
		QueueManager: queueManager,
		Mapper:       mapper,
	})

	tool.AddSystem(&systems.CodeDispatchSystem{
		QueueManager: queueManager,
		Mapper:       mapper,
	})

	// Add result systems (unchanged, they still use channels)
	tool.AddSystem(&systems.PulseResultSystem{
		ResultChan: pulseResults,
		Mapper:     mapper,
	})

	tool.AddSystem(&systems.InterventionResultSystem{
		ResultChan: interventionResults,
		Mapper:     mapper,
	})

	tool.AddSystem(&systems.CodeResultSystem{
		ResultChan: codeResults,
		Mapper:     mapper,
	})

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Initialize the world
	tool.Initialize()

	// Start monitoring loop
	go monitoringLoop(queueManager, memManager)

	// Main application loop
	mainLoop(tool, sigChan)

	// Graceful shutdown
	fmt.Println("Starting graceful shutdown...")

	// Log final metrics
	finalMetrics := queueManager.GetMetrics()
	controller.SystemLogger.Info("Final queue metrics: %+v", finalMetrics)

	tool.Finalize()
	memManager.LogMemoryStats()

	fmt.Println("Shutdown complete")
}

func mainLoop(tool *app.App, sigChan chan os.Signal) {
	ticker := time.NewTicker(time.Second / time.Duration(tool.TPS))
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			fmt.Println("Shutdown signal received")
			return
		case <-ticker.C:
			tool.Update()
		}
	}
}

func monitoringLoop(qm *queue.QueueManager, mm *controller.MemoryManager) {
	memTicker := time.NewTicker(10 * time.Second)
	metricsTicker := time.NewTicker(30 * time.Second)
	defer memTicker.Stop()
	defer metricsTicker.Stop()

	for {
		select {
		case <-memTicker.C:
			mm.MonitorMemory()
		case <-metricsTicker.C:
			metrics := qm.GetMetrics()

			// Calculate throughput
			processed := metrics["pulsesProcessed"].(int64)
			queued := metrics["pulsesQueued"].(int64)
			dropped := metrics["droppedJobs"].(int64)

			var efficiency float64
			if queued > 0 {
				efficiency = float64(processed) / float64(queued) * 100
			}

			controller.SystemLogger.Info(
				"Queue Performance: Processed=%d, Queued=%d, Dropped=%d, Efficiency=%.1f%%, QueueDepth=%d",
				processed, queued, dropped, efficiency, metrics["pulseQueueDepth"],
			)
		}
	}
}
