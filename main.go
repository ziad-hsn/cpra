package main

import (
	"bufio"
	"cpra/internal/controller"
	"cpra/internal/controller/entities"
	"cpra/internal/controller/systems"
	"cpra/internal/loader/loader"
	"cpra/internal/loader/schema"
	"cpra/internal/queue"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark-tools/app"
	"gopkg.in/yaml.v3"
)

func main() {
	// Check for debug mode
	debugMode := os.Getenv("CPRA_DEBUG") == "true" ||
		(len(os.Args) > 1 && (os.Args[1] == "--debug" || os.Args[1] == "-d"))

	// Check for tracing mode
	tracingEnabled := debugMode || (os.Getenv("CPRA_TRACING") == "true")

	// Initialize logging and tracing
	controller.InitializeLoggers(debugMode)
	controller.InitializeTracers(tracingEnabled)
	defer controller.CloseLoggers()
	
	// Start periodic trace cleanup if tracing is enabled
	if tracingEnabled {
		controller.StartPeriodicCleanup(5*time.Minute, 30*time.Minute)
	}

	controller.SystemLogger.Info("Starting CPRa Monitoring System with Optimized Queue")

	// Set optimal GC and memory settings for high throughput
	debug.SetGCPercent(50)               // More aggressive GC for queue-heavy workload
	runtime.GOMAXPROCS(runtime.NumCPU()) // Use all available cores

	// Memory manager with 2GB limit for 10k+ monitors
	memManager := controller.NewMemoryManager(2, 30)
	memManager.SetMemoryLimit()

	// Load configuration with streaming for large files
	configFile := "internal/loader/replicated_test.yaml"
	
	// Check file size first
	fileInfo, err := os.Stat(configFile)
	if err != nil {
		controller.SystemLogger.Fatal("Failed to stat config file: %v", err)
	}
	
	// Create ECS world and systems first
	tool := app.New(1024).Seed(123)
	tool.TPS = 10000 // High TPS for 10k monitors with 1s intervals
	
	var manifest schema.Manifest
	var monitorCount int
	
	// Use streaming if file > 50MB
	if fileInfo.Size() > 50*1024*1024 {
		controller.SystemLogger.Info("Large file detected (%d bytes) - using streaming parse and create", fileInfo.Size())
		monitorCount, err = streamParseAndCreate(configFile, &tool.World)
		if err != nil {
			controller.SystemLogger.Fatal("Failed to stream load: %v", err)
		}
		// Create empty manifest since entities are already created
		manifest = schema.Manifest{Monitors: make([]schema.Monitor, 0)}
	} else {
		controller.SystemLogger.Info("Small file (%d bytes) - using standard loading", fileInfo.Size())
		l := loader.NewLoader("yaml", configFile)
		if err := l.Load(); err != nil {
			controller.SystemLogger.Fatal("Failed to load configuration: %v", err)
		}
		manifest = l.GetManifest()
		monitorCount = len(manifest.Monitors)
	}

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

	// Create entities (already done for large files via streaming)
	if fileInfo.Size() <= 50*1024*1024 {
		// Only create entities for small files (large files already created during streaming)
		_, err = controller.NewCPRaWorld(&manifest, &tool.World)
		if err != nil {
			controller.SystemLogger.Fatal("Failed to create world: %v", err)
		}
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

// Batch entity creation for large monitor counts (only in main.go)
func createEntitiesWithBatches(manifest *schema.Manifest, world *ecs.World) error {
	mapper := entities.InitializeMappers(world)
	start := time.Now()
	
	batchSize := 1000
	workerCount := 8
	totalMonitors := len(manifest.Monitors)
	
	var created atomic.Int64
	var errors atomic.Int64
	
	workChan := make(chan []schema.Monitor, 10)
	
	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			for batch := range workChan {
				for _, monitor := range batch {
					err := mapper.CreateEntityFromMonitor(monitor, world)
					if err != nil {
						errors.Add(1)
						controller.SystemLogger.Error("Failed to create entity for monitor %s: %v", monitor.Name, err)
					} else {
						created.Add(1)
					}
				}
			}
		}(i)
	}
	
	// Start progress reporter
	progressDone := make(chan bool)
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				currentCreated := created.Load()
				currentErrors := errors.Load()
				elapsed := time.Since(start)
				rate := float64(currentCreated) / elapsed.Seconds()
				
				var percentage float64
				var eta time.Duration
				if totalMonitors > 0 {
					percentage = float64(currentCreated) / float64(totalMonitors) * 100
					if rate > 0 {
						remaining := float64(totalMonitors) - float64(currentCreated)
						eta = time.Duration(remaining/rate) * time.Second
					}
				}
				
				controller.SystemLogger.Info("Entity progress: %d/%d (%.1f%%) created, %d errors, %.1f/sec, ETA: %v",
					currentCreated, totalMonitors, percentage, currentErrors, rate, eta)
			}
		}
	}()
	
	// Send work in batches
	go func() {
		defer close(workChan)
		
		for i := 0; i < totalMonitors; i += batchSize {
			end := i + batchSize
			if end > totalMonitors {
				end = totalMonitors
			}
			
			batch := manifest.Monitors[i:end]
			workChan <- batch
		}
	}()
	
	// Wait for completion
	wg.Wait()
	close(progressDone)
	
	// Final statistics
	elapsed := time.Since(start)
	finalCreated := created.Load()
	finalErrors := errors.Load()
	rate := float64(finalCreated) / elapsed.Seconds()
	
	controller.SystemLogger.Info("Batch entity creation complete: %d entities created, %d errors in %v (%.1f/sec)", 
		finalCreated, finalErrors, elapsed, rate)
	
	if finalErrors > 0 {
		return fmt.Errorf("entity creation completed with %d errors", finalErrors)
	}
	
	return nil
}

// True streaming: parse 1 monitor and immediately create entity
func streamParseAndCreate(filename string, world *ecs.World) (int, error) {
	file, err := os.Open(filename)
	if err != nil {
		return 0, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	mapper := entities.InitializeMappers(world)
	scanner := bufio.NewScanner(file)
	
	var currentYAML strings.Builder
	var monitorLevel int = -1
	var inMonitorsList bool = false
	var monitorCount int
	var errorCount int
	
	monitorRegex := regexp.MustCompile(`^(\s*)-\s+name:\s*(.+)$`)
	start := time.Now()
	
	// Progress reporting
	progressTicker := time.NewTicker(2 * time.Second)
	defer progressTicker.Stop()
	
	go func() {
		for range progressTicker.C {
			if monitorCount > 0 {
				elapsed := time.Since(start)
				rate := float64(monitorCount) / elapsed.Seconds()
				controller.SystemLogger.Info("Streaming progress: %d monitors created, %d errors, %.1f/sec",
					monitorCount, errorCount, rate)
			}
		}
	}()
	
	for scanner.Scan() {
		line := scanner.Text()
		
		// Detect monitors section
		if strings.HasPrefix(line, "monitors:") {
			inMonitorsList = true
			continue
		}
		
		if !inMonitorsList {
			continue
		}
		
		// Detect monitor start
		if match := monitorRegex.FindStringSubmatch(line); match != nil {
			// Parse and create previous monitor if exists
			if currentYAML.Len() > 0 {
				if parseAndCreateMonitor(currentYAML.String(), mapper, world) {
					monitorCount++
				} else {
					errorCount++
				}
				currentYAML.Reset()
			}
			
			// Start new monitor
			monitorLevel = len(match[1])
			// Remove the "- " prefix and add the content
			cleanLine := strings.TrimSpace(line)
			if strings.HasPrefix(cleanLine, "- ") {
				cleanLine = cleanLine[2:] // Remove "- " prefix
			}
			currentYAML.WriteString(cleanLine + "\n")
			continue
		}
		
		// Add lines belonging to current monitor
		if monitorLevel >= 0 {
			lineIndent := len(line) - len(strings.TrimLeft(line, " \t"))
			
			if strings.TrimSpace(line) != "" && lineIndent <= monitorLevel {
				// Parse and create current monitor, then start new one
				if currentYAML.Len() > 0 {
					if parseAndCreateMonitor(currentYAML.String(), mapper, world) {
						monitorCount++
					} else {
						errorCount++
					}
					currentYAML.Reset()
				}
				monitorLevel = -1
				
				// Check if this starts a new monitor
				if match := monitorRegex.FindStringSubmatch(line); match != nil {
					monitorLevel = len(match[1])
					cleanLine := strings.TrimSpace(line)
					if strings.HasPrefix(cleanLine, "- ") {
						cleanLine = cleanLine[2:] // Remove "- " prefix
					}
					currentYAML.WriteString(cleanLine + "\n")
				}
				continue
			}
			
			// Add line to current monitor - normalize indentation
			if lineIndent > monitorLevel && strings.TrimSpace(line) != "" {
				// Remove the base monitor indentation and preserve relative indentation
				relativeIndent := lineIndent - monitorLevel - 2 // -2 for the "- " that was removed
				if relativeIndent > 0 {
					currentYAML.WriteString(strings.Repeat(" ", relativeIndent) + strings.TrimSpace(line) + "\n")
				} else {
					currentYAML.WriteString(strings.TrimSpace(line) + "\n")
				}
			}
		}
	}
	
	// Parse final monitor
	if currentYAML.Len() > 0 {
		if parseAndCreateMonitor(currentYAML.String(), mapper, world) {
			monitorCount++
		} else {
			errorCount++
		}
	}
	
	elapsed := time.Since(start)
	rate := float64(monitorCount) / elapsed.Seconds()
	
	controller.SystemLogger.Info("Streaming complete: %d monitors created, %d errors in %v (%.1f/sec)",
		monitorCount, errorCount, elapsed, rate)
	
	if err := scanner.Err(); err != nil {
		return monitorCount, fmt.Errorf("scanner error: %w", err)
	}
	
	return monitorCount, nil
}

// Parse YAML and immediately create entity
func parseAndCreateMonitor(yamlContent string, mapper *entities.EntityManager, world *ecs.World) bool {
	var monitor schema.Monitor
	
	// Debug first few characters to see what we're trying to parse
	if len(yamlContent) > 100 {
		controller.SystemLogger.Debug("Parsing YAML: %s...", yamlContent[:100])
	}
	
	err := yaml.Unmarshal([]byte(yamlContent), &monitor)
	if err != nil {
		controller.SystemLogger.Error("Failed to parse monitor YAML: %v\nContent: %s", err, yamlContent[:min(200, len(yamlContent))])
		return false
	}
	
	err = mapper.CreateEntityFromMonitor(monitor, world)
	if err != nil {
		controller.SystemLogger.Error("Failed to create entity for monitor %s: %v", monitor.Name, err)
		return false
	}
	
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
