package main

import (
	"cpra/internal/controller"
	"cpra/internal/controller/entities"
	"cpra/internal/controller/systems"
	"cpra/internal/loader/loader"
	"cpra/internal/workers/workerspool"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/mlange-42/ark-tools/app"
)

// calculateOptimalWorkers determines worker count based on monitor scale and workload type
func calculateOptimalWorkers(monitorCount, cpuCount int) int {
	// For I/O-bound HTTP monitoring workloads, we can scale far beyond CPU count
	// Each worker spends most time waiting for network I/O, not CPU processing

	if monitorCount <= 100 {
		// Small scale: Conservative approach
		return cpuCount * 4
	} else if monitorCount <= 1000 {
		// Medium scale: Moderate scaling
		return cpuCount * 16
	} else if monitorCount <= 10000 {
		// High scale: Aggressive scaling for I/O bound workload
		// Use either 64x CPU count or 1 worker per 10 monitors, whichever is smaller
		return min(cpuCount*64, monitorCount/10)
	} else {
		// Very high scale: Maximum practical limit
		// Cap at 1000 workers to prevent resource exhaustion
		return min(1000, monitorCount/20)
	}
}

// calculateBufferSize determines optimal channel buffer sizes to prevent blocking
func calculateBufferSize(monitorCount int, poolType string) int {
	baseSize := monitorCount * 2 // Allow 2x monitor count for burst capacity

	switch poolType {
	case "pulse":
		// Pulse jobs are most frequent - need largest buffers
		if monitorCount <= 1000 {
			return max(baseSize, 50000)
		} else {
			return max(baseSize, 500000) // 500K buffer for 10K+ monitors
		}
	case "intervention":
		// Interventions are less frequent but critical
		return max(baseSize/4, 10000)
	case "code":
		// Code notifications can burst during outages
		return max(baseSize/2, 25000)
	default:
		return max(baseSize, 10000)
	}
}

func main() {
	// Check for debug mode from environment or command line
	debugMode := os.Getenv("CPRA_DEBUG") == "true" ||
		(len(os.Args) > 1 && (os.Args[1] == "--debug" || os.Args[1] == "-d"))

	// Initialize logging system
	controller.InitializeLoggers(debugMode)
	defer controller.CloseLoggers()

	controller.SystemLogger.Info("Starting CPRa Monitoring System")
	if debugMode {
		controller.SystemLogger.Debug("Debug mode enabled - verbose logging active")
	}

	// Production-ready settings with enhanced error handling
	debug.SetGCPercent(20)
	debug.SetMemoryLimit(1 << 30) // 1GB memory limit for stability

	// Initialize memory manager
	memManager := controller.NewMemoryManager(1, 30) // 1GB limit, GC every 30s
	memManager.SetMemoryLimit()
	controller.SystemLogger.Info("Memory manager initialized with 1GB limit")

	// Setup crash logging with rotation
	f, err := os.OpenFile("crash-latest.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	err = debug.SetCrashOutput(f, debug.CrashOptions{})
	if err != nil {
		log.Fatal(err)
	}

	// Load configuration with validation
	configFile := "internal/loader/sample.yaml"
	if debugMode {
		controller.SystemLogger.Debug("Loading configuration from: %s", configFile)
	}

	l := loader.NewLoader("yaml", configFile)
	if err := l.Load(); err != nil {
		controller.SystemLogger.Fatal("Failed to load configuration: %v", err)
	}
	manifest := l.GetManifest()

	if len(manifest.Monitors) == 0 {
		controller.SystemLogger.Fatal("No monitors configured")
	}

	controller.SystemLogger.Info("Configuration loaded successfully: %d monitors", len(manifest.Monitors))

	// Calculate optimal worker count for high-scale performance
	cpuCount := runtime.NumCPU()
	numWorkers := calculateOptimalWorkers(len(manifest.Monitors), cpuCount)

	controller.SystemLogger.Info("Worker scaling: %d workers for %d monitors (CPU: %d, ratio: %.1fx)",
		numWorkers, len(manifest.Monitors), cpuCount, float64(numWorkers)/float64(cpuCount))

	// Calculate optimal buffer sizes based on scale
	pulseBufferSize := calculateBufferSize(len(manifest.Monitors), "pulse")
	interventionBufferSize := calculateBufferSize(len(manifest.Monitors), "intervention")
	codeBufferSize := calculateBufferSize(len(manifest.Monitors), "code")

	controller.SystemLogger.Info("Channel buffers - Pulse: %d, Intervention: %d, Code: %d",
		pulseBufferSize, interventionBufferSize, codeBufferSize)

	// start workers pools with optimized buffers
	pools := workerspool.NewPoolsManager()
	pools.NewPool("pulse", numWorkers, pulseBufferSize, pulseBufferSize)
	pools.NewPool("intervention", numWorkers, interventionBufferSize, interventionBufferSize)
	pools.NewPool("code", numWorkers, codeBufferSize, codeBufferSize)

	pulseJobChan, err := pools.GetJobChannel("pulse")
	if err != nil {
		log.Fatal(err)
	}
	interventionJobChan, err := pools.GetJobChannel("intervention")
	if err != nil {
		log.Fatal(err)
	}
	CodeJobChan, err := pools.GetJobChannel("code")
	if err != nil {
		log.Fatal(err)
	}
	pulseResultChan, err := pools.GetResultChannel("pulse")
	if err != nil {
		log.Fatal(err)
	}
	interventionResultChan, err := pools.GetResultChannel("intervention")
	if err != nil {
		log.Fatal(err)
	}
	codeResultChan, err := pools.GetResultChannel("code")
	if err != nil {
		log.Fatal(err)
	}
	pools.StartAll()

	// Create a new, seeded tool.
	tool := app.New(1024).Seed(123)
	// Limit simulation speed.
	tool.TPS = 3000

	_, err = controller.NewCPRaWorld(&manifest, &tool.World)
	mapper := entities.InitializeMappers(&tool.World)

	tool.AddSystem(&systems.PulseScheduleSystem{
		Mapper: mapper,
	})
	tool.AddSystem(&systems.PulseDispatchSystem{
		JobChan: pulseJobChan,
		Mapper:  mapper,
	})

	tool.AddSystem(&systems.PulseResultSystem{
		ResultChan: pulseResultChan,
		Mapper:     mapper,
	})

	tool.AddSystem(&systems.InterventionDispatchSystem{
		JobChan: interventionJobChan,
		Mapper:  mapper,
	})
	tool.AddSystem(&systems.InterventionResultSystem{
		ResultChan: interventionResultChan,
		Mapper:     mapper,
	})
	tool.AddSystem(&systems.CodeDispatchSystem{
		JobChan: CodeJobChan,
		Mapper:  mapper,
	})

	tool.AddSystem(&systems.CodeResultSystem{
		ResultChan: codeResultChan,
		Mapper:     mapper,
	})

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Initialize the world
	tool.Initialize()

	// Main application loop
	mainLoop(tool, memManager, pools, sigChan)

	// Graceful shutdown sequence
	fmt.Println("Starting graceful shutdown...")

	// Stop worker pools first
	pools.StopAll()

	// Finalize simulation
	tool.Finalize()

	// Log final memory stats
	memManager.LogMemoryStats()

	fmt.Println("Shutdown complete")
}

func mainLoop(tool *app.App, memManager *controller.MemoryManager, pools *workerspool.PoolsManager, sigChan chan os.Signal) {
	memoryTicker := time.NewTicker(10 * time.Second)
	defer memoryTicker.Stop()

	poolStatsTicker := time.NewTicker(30 * time.Second)
	defer poolStatsTicker.Stop()

	ticker := time.NewTicker(time.Second / time.Duration(tool.TPS))
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			fmt.Println("Shutdown signal received, exiting main loop.")
			return
		case <-ticker.C:
			tool.Update()
		case <-memoryTicker.C:
			memManager.MonitorMemory()
		case <-poolStatsTicker.C:
			if pools.Monitor != nil {
				pools.Monitor.PrintStats()
			}
		}
	}
}
