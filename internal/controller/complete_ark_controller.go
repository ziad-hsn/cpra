// Package controller provides a complete CPRA controller using proper Ark ECS patterns
// Demonstrates correct usage of Ark batch operations for 1M+ monitor handling
package controller

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark/generic"

	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/controller/systems"
	"github.com/ziad-hsn/cpra/internal/queue"
	"github.com/ziad-hsn/cpra/internal/workers/workerspool"
)

// CompleteArkController demonstrates proper Ark ECS usage for CPRA
type CompleteArkController struct {
	// Core ECS
	world *ecs.World
	
	// Infrastructure
	queue      queue.QueueInterface
	workerPool *workerspool.WorkersPool
	
	// System orchestrator
	orchestrator *systems.ArkSystemOrchestrator
	
	// Performance monitoring
	startTime     time.Time
	updateCount   uint64
	lastStatsTime time.Time
	
	// Control
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewCompleteArkController creates a new controller with proper Ark patterns
func NewCompleteArkController(monitorCount int) *CompleteArkController {
	world := ecs.NewWorld()
	
	// Calculate optimal configuration
	workers := runtime.NumCPU() * 2
	queueSize := uint64(monitorCount / 10)
	if queueSize < 10000 {
		queueSize = 10000
	}
	
	// Create infrastructure using the fixed queue implementation
	queueConfig := queue.BoundedQueueConfig{
		MaxSize:      int(queueSize / 1000), // Number of batches
		MaxBatch:     1000,                  // Jobs per batch
		BatchTimeout: time.Millisecond * 10,
	}
	
	jobQueue := queue.NewFixedBoundedQueue(queueConfig)
	workerPool := workerspool.NewWorkersPool(workers, jobQueue)
	
	ctx, cancel := context.WithCancel(context.Background())
	
	return &CompleteArkController{
		world:        world,
		queue:        jobQueue,
		workerPool:   workerPool,
		orchestrator: systems.NewArkSystemOrchestrator(world, jobQueue, workerPool),
		startTime:    time.Now(),
		lastStatsTime: time.Now(),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// CreateMonitors creates monitors using Ark's optimized batch operations
func (c *CompleteArkController) CreateMonitors(count int) error {
	fmt.Printf("Creating %d monitors using Ark batch operations...\n", count)
	
	mapper := generic.NewMap1[components.MonitorState](c.world)
	
	// Use large batch sizes for optimal Ark performance (following research)
	batchSize := 10000
	created := 0
	
	for i := 0; i < count; i += batchSize {
		remaining := count - i
		if remaining > batchSize {
			remaining = batchSize
		}
		
		// Ark's most efficient entity creation method
		mapper.NewBatchFn(remaining, func(entity ecs.Entity, monitor *components.MonitorState) {
			*monitor = components.MonitorState{
				URL:      fmt.Sprintf("https://example.com/monitor-%d", created),
				Method:   "GET",
				Interval: time.Second * 30,
				Timeout:  time.Second * 5,
				
				// Set initial state
				Flags:     components.StateReady,
				NextCheck: time.Now().Add(time.Duration(created%30) * time.Second), // Spread load
				
				// Configuration
				InterventionThreshold: 3,
				AlertThreshold:        5,
			}
			created++
		})
		
		// Progress reporting for large batches
		if i%50000 == 0 && i > 0 {
			fmt.Printf("Created %d/%d monitors...\n", i, count)
		}
	}
	
	fmt.Printf("Successfully created %d monitors using Ark batch operations\n", created)
	return nil
}

// Start begins the controller operation
func (c *CompleteArkController) Start() error {
	fmt.Printf("Starting complete Ark controller...\n")
	
	// Start worker pool
	if err := c.workerPool.Start(); err != nil {
		return fmt.Errorf("failed to start worker pool: %w", err)
	}
	
	// Start system orchestrator
	c.orchestrator.Start()
	
	// Start main monitoring loop
	c.wg.Add(1)
	go c.monitoringLoop()
	
	fmt.Printf("Controller started successfully\n")
	return nil
}

// monitoringLoop provides performance monitoring and statistics
func (c *CompleteArkController) monitoringLoop() {
	defer c.wg.Done()
	
	ticker := time.NewTicker(time.Second * 10) // Stats every 10 seconds
	defer ticker.Stop()
	
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.printStats()
		}
	}
}

// printStats displays comprehensive performance statistics
func (c *CompleteArkController) printStats() {
	uptime := time.Since(c.startTime)
	systemStats := c.orchestrator.Stats()
	queueStats := c.queue.Stats()
	
	// Get entity count using Ark query
	filter := generic.NewFilter1[components.MonitorState](c.world)
	query := filter.Query()
	
	var totalEntities, readyEntities, processingEntities, failedEntities int
	for query.Next() {
		monitor := query.Get()
		totalEntities++
		
		if monitor.IsReady() {
			readyEntities++
		} else if monitor.IsProcessing() || monitor.HasPendingJobs() {
			processingEntities++
		} else if monitor.IsFailed() {
			failedEntities++
		}
	}
	query.Close()
	
	fmt.Printf("\n=== CPRA Complete Ark Controller Stats ===\n")
	fmt.Printf("Uptime: %v\n", uptime)
	fmt.Printf("Total Entities: %d\n", totalEntities)
	fmt.Printf("  Ready: %d (%.1f%%)\n", readyEntities, float64(readyEntities)/float64(totalEntities)*100)
	fmt.Printf("  Processing: %d (%.1f%%)\n", processingEntities, float64(processingEntities)/float64(totalEntities)*100)
	fmt.Printf("  Failed: %d (%.1f%%)\n", failedEntities, float64(failedEntities)/float64(totalEntities)*100)
	
	fmt.Printf("\nSystem Performance:\n")
	fmt.Printf("  Pulse Scheduled: %d\n", systemStats.PulseScheduled)
	fmt.Printf("  Pulse Dropped: %d\n", systemStats.PulseDropped)
	fmt.Printf("  Interventions: %d\n", systemStats.InterventionScheduled)
	fmt.Printf("  Alerts: %d\n", systemStats.CodeScheduled)
	fmt.Printf("  Results Processed: %d\n", systemStats.ResultsProcessed)
	
	fmt.Printf("\nQueue Performance:\n")
	fmt.Printf("  Enqueued: %d\n", queueStats.Enqueued)
	fmt.Printf("  Dequeued: %d\n", queueStats.Dequeued)
	fmt.Printf("  Dropped: %d\n", queueStats.Dropped)
	fmt.Printf("  Queue Depth: %d/%d (%.1f%%)\n", 
		queueStats.QueueDepth, queueStats.BatchCount, 
		float64(queueStats.QueueDepth)/float64(queueStats.BatchCount)*100)
	
	if queueStats.Dequeued > 0 {
		throughput := float64(queueStats.Dequeued) / uptime.Seconds()
		fmt.Printf("  Throughput: %.1f jobs/sec\n", throughput)
		fmt.Printf("  Success Rate: %.2f%%\n", 
			float64(queueStats.Dequeued-queueStats.Dropped)/float64(queueStats.Dequeued)*100)
	}
	
	fmt.Printf("==========================================\n\n")
}

// Stop gracefully shuts down the controller
func (c *CompleteArkController) Stop() error {
	fmt.Printf("Stopping complete Ark controller...\n")
	
	// Stop monitoring
	c.cancel()
	c.wg.Wait()
	
	// Stop systems
	c.orchestrator.Stop()
	
	// Stop worker pool
	if err := c.workerPool.Stop(); err != nil {
		return fmt.Errorf("failed to stop worker pool: %w", err)
	}
	
	// Close queue
	c.queue.Close()
	
	fmt.Printf("Controller stopped successfully\n")
	return nil
}

// GetStats returns current performance statistics
func (c *CompleteArkController) GetStats() ControllerStats {
	systemStats := c.orchestrator.Stats()
	queueStats := c.queue.Stats()
	
	return ControllerStats{
		Uptime:           time.Since(c.startTime),
		SystemStats:      systemStats,
		QueueStats:       queueStats,
		UpdateCount:      atomic.LoadUint64(&c.updateCount),
	}
}

// ControllerStats provides comprehensive controller statistics
type ControllerStats struct {
	Uptime      time.Duration           `json:"uptime"`
	SystemStats systems.SystemStats     `json:"system_stats"`
	QueueStats  queue.Stats            `json:"queue_stats"`
	UpdateCount uint64                 `json:"update_count"`
}

// =============================================================================
// DEMONSTRATION MAIN FUNCTION
// =============================================================================

// DemoCompleteArkController demonstrates the complete Ark-based implementation
func DemoCompleteArkController() {
	fmt.Println("=== CPRA Complete Ark Controller Demo ===")
	fmt.Println("Demonstrating proper Ark ECS patterns for 1M+ monitors")
	fmt.Println()
	
	// Test with configurable monitor count
	monitorCount := 100000 // Start with 100K for demo
	
	controller := NewCompleteArkController(monitorCount)
	
	// Create monitors using optimized batch operations
	fmt.Printf("Creating %d monitors...\n", monitorCount)
	start := time.Now()
	
	if err := controller.CreateMonitors(monitorCount); err != nil {
		fmt.Printf("Error creating monitors: %v\n", err)
		return
	}
	
	creationTime := time.Since(start)
	fmt.Printf("Monitor creation completed in %v (%.0f monitors/sec)\n", 
		creationTime, float64(monitorCount)/creationTime.Seconds())
	
	// Start the controller
	if err := controller.Start(); err != nil {
		fmt.Printf("Error starting controller: %v\n", err)
		return
	}
	
	// Run for demonstration period
	fmt.Printf("Running controller for 2 minutes...\n")
	time.Sleep(time.Minute * 2)
	
	// Stop the controller
	if err := controller.Stop(); err != nil {
		fmt.Printf("Error stopping controller: %v\n", err)
		return
	}
	
	// Final statistics
	finalStats := controller.GetStats()
	fmt.Printf("\n=== Final Performance Summary ===\n")
	fmt.Printf("Total Runtime: %v\n", finalStats.Uptime)
	fmt.Printf("Total Jobs Processed: %d\n", finalStats.QueueStats.Dequeued)
	fmt.Printf("Average Throughput: %.1f jobs/sec\n", 
		float64(finalStats.QueueStats.Dequeued)/finalStats.Uptime.Seconds())
	fmt.Printf("Success Rate: %.2f%%\n", 
		float64(finalStats.QueueStats.Dequeued-finalStats.QueueStats.Dropped)/float64(finalStats.QueueStats.Dequeued)*100)
	
	fmt.Println("\n=== Demo completed successfully! ===")
}

