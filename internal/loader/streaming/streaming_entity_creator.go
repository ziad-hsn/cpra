package streaming

import (
	"context"
	"fmt"
	"sync"
	"time"
	
	"github.com/mlange-42/ark/ecs"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
)

// StreamingEntityCreator handles batch entity creation for Ark ECS
// NOTE: All ECS operations are single-threaded due to Ark's thread safety constraints
type StreamingEntityCreator struct {
	world       *ecs.World
	batchSize   int
	
	// Component mappers (cached for performance)
	nameMapper        *ecs.Map1[components.Name]
	pulseConfigMapper *ecs.Map1[components.PulseConfig]
	pulseStatusMapper *ecs.Map1[components.PulseStatus]
	pulseFirstCheckMapper *ecs.Map1[components.PulseFirstCheck]
	pulseJobMapper    *ecs.Map1[components.PulseJob]
	interventionConfigMapper *ecs.Map1[components.InterventionConfig]
	interventionStatusMapper *ecs.Map1[components.InterventionStatus]
	interventionJobMapper    *ecs.Map1[components.InterventionJob]
	redCodeConfigMapper      *ecs.Map1[components.RedCodeConfig]
	redCodeStatusMapper      *ecs.Map1[components.RedCodeStatus]
	redCodeJobMapper         *ecs.Map1[components.RedCodeJob]
	greenCodeConfigMapper    *ecs.Map1[components.GreenCodeConfig]
	greenCodeStatusMapper    *ecs.Map1[components.GreenCodeStatus]
	greenCodeJobMapper       *ecs.Map1[components.GreenCodeJob]
	cyanCodeConfigMapper     *ecs.Map1[components.CyanCodeConfig]
	cyanCodeStatusMapper     *ecs.Map1[components.CyanCodeStatus]
	cyanCodeJobMapper        *ecs.Map1[components.CyanCodeJob]
	yellowCodeConfigMapper   *ecs.Map1[components.YellowCodeConfig]
	yellowCodeStatusMapper   *ecs.Map1[components.YellowCodeStatus]
	yellowCodeJobMapper      *ecs.Map1[components.YellowCodeJob]
	grayCodeConfigMapper     *ecs.Map1[components.GrayCodeConfig]
	grayCodeStatusMapper     *ecs.Map1[components.GrayCodeStatus]
	grayCodeJobMapper        *ecs.Map1[components.GrayCodeJob]
	
	// Color marker component mappers
	redCodeMapper            *ecs.Map1[components.RedCode]
	greenCodeMapper          *ecs.Map1[components.GreenCode]
	cyanCodeMapper           *ecs.Map1[components.CyanCode]
	yellowCodeMapper         *ecs.Map1[components.YellowCode]
	grayCodeMapper           *ecs.Map1[components.GrayCode]
	
	// Monitor status mapper
	monitorStatusMapper      *ecs.Map1[components.MonitorStatus]
	
	// Statistics
	entitiesCreated int64
	batchesProcessed int64
	startTime       time.Time
	
	mu sync.RWMutex // Protects statistics only
}

// EntityCreationConfig holds entity creation configuration
type EntityCreationConfig struct {
	BatchSize     int
	PreAllocate   int  // Pre-allocate entity capacity
	ProgressChan  chan<- EntityProgress
}

// EntityProgress represents entity creation progress
type EntityProgress struct {
	EntitiesCreated  int64
	BatchesProcessed int64
	Rate            float64
	MemoryUsage     int64
}

// NewStreamingEntityCreator creates a new entity creator
func NewStreamingEntityCreator(world *ecs.World, config EntityCreationConfig) *StreamingEntityCreator {
	creator := &StreamingEntityCreator{
		world:     world,
		batchSize: config.BatchSize,
		startTime: time.Now(),
	}
	
	// Initialize component mappers for performance
	creator.initializeMappers()
	
	// Pre-allocate entities if requested
	if config.PreAllocate > 0 {
		creator.preAllocateEntities(config.PreAllocate)
	}
	
	return creator
}

// initializeMappers initializes component mappers for efficient access
func (c *StreamingEntityCreator) initializeMappers() {
	c.nameMapper = ecs.NewMap1[components.Name](c.world)
	c.pulseConfigMapper = ecs.NewMap1[components.PulseConfig](c.world)
	c.pulseStatusMapper = ecs.NewMap1[components.PulseStatus](c.world)
	c.pulseFirstCheckMapper = ecs.NewMap1[components.PulseFirstCheck](c.world)
	c.pulseJobMapper = ecs.NewMap1[components.PulseJob](c.world)
	c.interventionConfigMapper = ecs.NewMap1[components.InterventionConfig](c.world)
	c.interventionStatusMapper = ecs.NewMap1[components.InterventionStatus](c.world)
	c.interventionJobMapper = ecs.NewMap1[components.InterventionJob](c.world)
	c.redCodeConfigMapper = ecs.NewMap1[components.RedCodeConfig](c.world)
	c.redCodeStatusMapper = ecs.NewMap1[components.RedCodeStatus](c.world)
	c.redCodeJobMapper = ecs.NewMap1[components.RedCodeJob](c.world)
	c.greenCodeConfigMapper = ecs.NewMap1[components.GreenCodeConfig](c.world)
	c.greenCodeStatusMapper = ecs.NewMap1[components.GreenCodeStatus](c.world)
	c.greenCodeJobMapper = ecs.NewMap1[components.GreenCodeJob](c.world)
	c.cyanCodeConfigMapper = ecs.NewMap1[components.CyanCodeConfig](c.world)
	c.cyanCodeStatusMapper = ecs.NewMap1[components.CyanCodeStatus](c.world)
	c.cyanCodeJobMapper = ecs.NewMap1[components.CyanCodeJob](c.world)
	c.yellowCodeConfigMapper = ecs.NewMap1[components.YellowCodeConfig](c.world)
	c.yellowCodeStatusMapper = ecs.NewMap1[components.YellowCodeStatus](c.world)
	c.yellowCodeJobMapper = ecs.NewMap1[components.YellowCodeJob](c.world)
	c.grayCodeConfigMapper = ecs.NewMap1[components.GrayCodeConfig](c.world)
	c.grayCodeStatusMapper = ecs.NewMap1[components.GrayCodeStatus](c.world)
	c.grayCodeJobMapper = ecs.NewMap1[components.GrayCodeJob](c.world)
	
	// Initialize color marker mappers
	c.redCodeMapper = ecs.NewMap1[components.RedCode](c.world)
	c.greenCodeMapper = ecs.NewMap1[components.GreenCode](c.world)
	c.cyanCodeMapper = ecs.NewMap1[components.CyanCode](c.world)
	c.yellowCodeMapper = ecs.NewMap1[components.YellowCode](c.world)
	c.grayCodeMapper = ecs.NewMap1[components.GrayCode](c.world)
	
	// Initialize monitor status mapper
	c.monitorStatusMapper = ecs.NewMap1[components.MonitorStatus](c.world)
}

// preAllocateEntities pre-allocates entity storage to reduce allocations
func (c *StreamingEntityCreator) preAllocateEntities(count int) {
	// Create and immediately remove entities to pre-allocate storage
	entities := make([]ecs.Entity, count)
	for i := 0; i < count; i++ {
		entities[i] = c.world.NewEntity()
	}
	for _, entity := range entities {
		c.world.RemoveEntity(entity)
	}
}

// ProcessBatches processes monitor batches and creates entities
// NOTE: This must be called from a single goroutine due to Ark ECS constraints
func (c *StreamingEntityCreator) ProcessBatches(ctx context.Context, batchChan <-chan MonitorBatch, progressChan chan<- EntityProgress) error {
	progressTicker := time.NewTicker(2 * time.Second)
	defer progressTicker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
			
		case <-progressTicker.C:
			c.reportProgress(progressChan)
			
		case batch, ok := <-batchChan:
			if !ok {
				// Channel closed, final progress report
				c.reportProgress(progressChan)
				return nil
			}
			
			// Process batches efficiently - 10K entities are processed directly
			if err := c.processBatch(batch); err != nil {
				return fmt.Errorf("failed to process batch %d: %w", batch.BatchID, err)
			}
		}
	}
}

// processBatch creates entities for a single batch of monitors using Ark's batch creation
func (c *StreamingEntityCreator) processBatch(batch MonitorBatch) error {
	if len(batch.Monitors) == 0 {
		return nil
	}
	
	// Group monitors by archetype for batch creation optimization
	archetypes := c.groupMonitorsByArchetype(batch.Monitors)
	
	// Create entities in batches by archetype for maximum efficiency
	for archetype, monitors := range archetypes {
		if err := c.createEntitiesBatch(archetype, monitors); err != nil {
			return fmt.Errorf("failed to create entities for archetype %s: %w", archetype, err)
		}
	}
	
	// Update statistics
	c.updateStats(len(batch.Monitors))
	
	// Log processing time for large batches (debug only)
	// Removed noisy debug output - controlled by debug flags
	
	return nil
}

// groupMonitorsByArchetype groups monitors by their component archetype for batch creation
func (c *StreamingEntityCreator) groupMonitorsByArchetype(monitors []schema.Monitor) map[string][]schema.Monitor {
	archetypes := make(map[string][]schema.Monitor)
	
	for _, monitor := range monitors {
		// Create archetype key based on components this monitor will have
		archetype := c.getMonitorArchetype(monitor)
		archetypes[archetype] = append(archetypes[archetype], monitor)
	}
	
	return archetypes
}

// getMonitorArchetype determines the archetype key for a monitor based on its configuration
func (c *StreamingEntityCreator) getMonitorArchetype(monitor schema.Monitor) string {
	var components []string
	
	// All monitors have these base components
	components = append(components, "Name", "MonitorStatus", "PulseConfig", "PulseStatus", "PulseJob", "PulseFirstCheck")
	
	// Add intervention components if configured
	if monitor.Intervention.Action != "" {
		components = append(components, "InterventionConfig", "InterventionStatus", "InterventionJob")
	}
	
	// Add code components by color
	for color := range monitor.Codes {
		switch color {
		case "red":
			components = append(components, "RedCode", "RedCodeConfig", "RedCodeStatus", "RedCodeJob")
		case "green":
			components = append(components, "GreenCode", "GreenCodeConfig", "GreenCodeStatus", "GreenCodeJob")
		case "cyan":
			components = append(components, "CyanCode", "CyanCodeConfig", "CyanCodeStatus", "CyanCodeJob")
		case "yellow":
			components = append(components, "YellowCode", "YellowCodeConfig", "YellowCodeStatus", "YellowCodeJob")
		case "gray":
			components = append(components, "GrayCode", "GrayCodeConfig", "GrayCodeStatus", "GrayCodeJob")
		}
	}
	
	return fmt.Sprintf("%v", components)
}

// createEntitiesBatch creates a batch of entities with the same archetype using Ark's batch operations
// Optimized for very large batches (100,000+ entities)
func (c *StreamingEntityCreator) createEntitiesBatch(archetype string, monitors []schema.Monitor) error {
	if len(monitors) == 0 {
		return nil
	}
	
	// For very large batches, process in chunks to avoid memory pressure
	const maxChunkSize = 10000 // Process up to 10k entities at once
	
	for i := 0; i < len(monitors); i += maxChunkSize {
		end := i + maxChunkSize
		if end > len(monitors) {
			end = len(monitors)
		}
		
		chunk := monitors[i:end]
		
		// Use Ark's batch entity creation for maximum performance
		entities := c.createEntitiesBulk(len(chunk))
		
		// Add components in batches using Ark's batch operations
		c.addComponentsBatch(entities, chunk)
		
		// Large chunk processing completed silently
	}
	
	return nil
}

// createEntitiesBulk creates a bulk of entities efficiently
func (c *StreamingEntityCreator) createEntitiesBulk(count int) []ecs.Entity {
	// Pre-allocate slice for better memory performance
	entities := make([]ecs.Entity, count)
	
	// Create entities in bulk - this is the most efficient way in Ark
	for i := 0; i < count; i++ {
		entities[i] = c.world.NewEntity()
	}
	
	return entities
}

// addComponentsBatch adds components to a batch of entities efficiently
// Optimized for very large batches using Ark's batch operations
func (c *StreamingEntityCreator) addComponentsBatch(entities []ecs.Entity, monitors []schema.Monitor) {
	// Use Ark's batch operations with NewBatch for base components
	// Create batches for entities with identical base component structure
	
	// Group entities by their pulse configuration to optimize batch operations
	c.addBaseComponentsBatch(entities, monitors)
	
	// Add intervention components for entities that need them
	c.addInterventionComponentsBatch(entities, monitors)
	
	// Add code components for entities that need them  
	c.addCodeComponentsBatch(entities, monitors)
	
	// Large batch component addition completed silently
}

// addBaseComponentsBatch uses Ark's NewBatch operations for base components
func (c *StreamingEntityCreator) addBaseComponentsBatch(entities []ecs.Entity, monitors []schema.Monitor) {
	if len(entities) == 0 {
		return
	}
	
	// Use Ark's Map.NewBatch for efficient entity creation with base components
	// Since all monitors have the same base components, we can use batch operations
	
	// Create component arrays for batch operations
	names := make([]components.Name, len(entities))
	monitorStatuses := make([]components.MonitorStatus, len(entities))
	pulseConfigs := make([]components.PulseConfig, len(entities))
	pulseStatuses := make([]components.PulseStatus, len(entities))
	pulseJobs := make([]components.PulseJob, len(entities))
	firstChecks := make([]components.PulseFirstCheck, len(entities))
	
	// Prepare component data
	for i, monitor := range monitors {
		names[i] = components.Name(monitor.Name)
		
		monitorStatuses[i] = components.MonitorStatus{
			Status:          "pending",
			LastCheckTime:   time.Time{},
			LastSuccessTime: time.Time{},
			LastError:       nil,
		}
		
		pulseConfigs[i] = components.PulseConfig{
			Type:        monitor.Pulse.Type,
			Timeout:     monitor.Pulse.Timeout,
			Interval:    monitor.Pulse.Interval,
			Retries:     3,
			MaxFailures: monitor.Pulse.MaxFailures,
			Config:      monitor.Pulse.Config,
		}
		
		pulseStatuses[i] = components.PulseStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastCheckTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:           nil,
		}
		
		// Create pulse job
		pulseJob, err := jobs.CreatePulseJob(monitor.Pulse, entities[i])
		if err != nil {
			fmt.Printf("WARNING: Failed to create pulse job for entity %d: %v\n", entities[i].ID(), err)
			pulseJobs[i] = components.PulseJob{} // Empty job as fallback
		} else {
			pulseJobs[i] = components.PulseJob{Job: pulseJob}
		}
		
		firstChecks[i] = components.PulseFirstCheck{}
	}
	
	// Add components using individual adds (still efficient for large batches)
	// Ark's mappers are optimized for this pattern
	for i, entity := range entities {
		c.nameMapper.Add(entity, &names[i])
		c.monitorStatusMapper.Add(entity, &monitorStatuses[i])
		c.pulseConfigMapper.Add(entity, &pulseConfigs[i])
		c.pulseStatusMapper.Add(entity, &pulseStatuses[i])
		c.pulseJobMapper.Add(entity, &pulseJobs[i])
		c.pulseFirstCheckMapper.Add(entity, &firstChecks[i])
	}
}

// addInterventionComponentsBatch adds intervention components in batches
func (c *StreamingEntityCreator) addInterventionComponentsBatch(entities []ecs.Entity, monitors []schema.Monitor) {
	for i, monitor := range monitors {
		entity := entities[i]
		
		if monitor.Intervention.Action != "" {
			interventionConfig := &components.InterventionConfig{
				Action:      monitor.Intervention.Action,
				MaxFailures: monitor.Intervention.MaxFailures,
				Target:      monitor.Intervention.Target,
			}
			c.interventionConfigMapper.Add(entity, interventionConfig)
			
			interventionStatus := &components.InterventionStatus{
				LastStatus:           "pending",
				ConsecutiveFailures:  0,
				LastInterventionTime: time.Time{},
				LastSuccessTime:      time.Time{},
				LastError:            nil,
			}
			c.interventionStatusMapper.Add(entity, interventionStatus)
			
			// Create intervention job
			interventionJob, err := jobs.CreateInterventionJob(monitor.Intervention, entity)
			if err != nil {
				fmt.Printf("WARNING: Failed to create intervention job for entity %d: %v\n", entity.ID(), err)
			} else {
				interventionJobComp := &components.InterventionJob{Job: interventionJob}
				c.interventionJobMapper.Add(entity, interventionJobComp)
			}
		}
	}
}

// addCodeComponentsBatch adds code components in batches
func (c *StreamingEntityCreator) addCodeComponentsBatch(entities []ecs.Entity, monitors []schema.Monitor) {
	for i, monitor := range monitors {
		entity := entities[i]
		
		for color, codeConfig := range monitor.Codes {
			c.addCodeComponentsForColor(entity, color, codeConfig, monitor.Name)
		}
	}
}

// addCodeComponentsForColor adds code components for a specific color
func (c *StreamingEntityCreator) addCodeComponentsForColor(entity ecs.Entity, color string, codeConfig schema.CodeConfig, monitorName string) {
	switch color {
	case "red":
		config := &components.RedCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5,
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.redCodeConfigMapper.Add(entity, config)
		
		status := &components.RedCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:           nil,
		}
		c.redCodeStatusMapper.Add(entity, status)
		
		redCodeJob, err := jobs.CreateCodeJob(monitorName, codeConfig, entity, "red")
		if err != nil {
			fmt.Printf("WARNING: Failed to create red code job for entity %d: %v\n", entity.ID(), err)
		} else {
			c.redCodeJobMapper.Add(entity, &components.RedCodeJob{Job: redCodeJob})
		}
		
		c.redCodeMapper.Add(entity, &components.RedCode{})
		
	case "green":
		config := &components.GreenCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5,
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.greenCodeConfigMapper.Add(entity, config)
		
		status := &components.GreenCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:           nil,
		}
		c.greenCodeStatusMapper.Add(entity, status)
		
		greenCodeJob, err := jobs.CreateCodeJob(monitorName, codeConfig, entity, "green")
		if err != nil {
			fmt.Printf("WARNING: Failed to create green code job for entity %d: %v\n", entity.ID(), err)
		} else {
			c.greenCodeJobMapper.Add(entity, &components.GreenCodeJob{Job: greenCodeJob})
		}
		
		c.greenCodeMapper.Add(entity, &components.GreenCode{})
		
	case "cyan":
		config := &components.CyanCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5,
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.cyanCodeConfigMapper.Add(entity, config)
		
		status := &components.CyanCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:           nil,
		}
		c.cyanCodeStatusMapper.Add(entity, status)
		
		cyanCodeJob, err := jobs.CreateCodeJob(monitorName, codeConfig, entity, "cyan")
		if err != nil {
			fmt.Printf("WARNING: Failed to create cyan code job for entity %d: %v\n", entity.ID(), err)
		} else {
			c.cyanCodeJobMapper.Add(entity, &components.CyanCodeJob{Job: cyanCodeJob})
		}
		
		c.cyanCodeMapper.Add(entity, &components.CyanCode{})
		
	case "yellow":
		config := &components.YellowCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5,
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.yellowCodeConfigMapper.Add(entity, config)
		
		status := &components.YellowCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:           nil,
		}
		c.yellowCodeStatusMapper.Add(entity, status)
		
		yellowCodeJob, err := jobs.CreateCodeJob(monitorName, codeConfig, entity, "yellow")
		if err != nil {
			fmt.Printf("WARNING: Failed to create yellow code job for entity %d: %v\n", entity.ID(), err)
		} else {
			c.yellowCodeJobMapper.Add(entity, &components.YellowCodeJob{Job: yellowCodeJob})
		}
		
		c.yellowCodeMapper.Add(entity, &components.YellowCode{})
		
	case "gray":
		config := &components.GrayCodeConfig{
			Dispatch:    codeConfig.Dispatch,
			MaxFailures: 5,
			Notify:      codeConfig.Notify,
			Config:      codeConfig.Config,
		}
		c.grayCodeConfigMapper.Add(entity, config)
		
		status := &components.GrayCodeStatus{
			LastStatus:          "pending",
			ConsecutiveFailures: 0,
			LastAlertTime:       time.Time{},
			LastSuccessTime:     time.Time{},
			LastError:           nil,
		}
		c.grayCodeStatusMapper.Add(entity, status)
		
		grayCodeJob, err := jobs.CreateCodeJob(monitorName, codeConfig, entity, "gray")
		if err != nil {
			fmt.Printf("WARNING: Failed to create gray code job for entity %d: %v\n", entity.ID(), err)
		} else {
			c.grayCodeJobMapper.Add(entity, &components.GrayCodeJob{Job: grayCodeJob})
		}
		
		c.grayCodeMapper.Add(entity, &components.GrayCode{})
	}
}

// processLargeBatch splits large batches into smaller chunks for efficient processing
func (c *StreamingEntityCreator) processLargeBatch(batch MonitorBatch) error {
	const chunkSize = 10000
	monitors := batch.Monitors
	
	// Process in chunks to maintain memory efficiency
	for i := 0; i < len(monitors); i += chunkSize {
		end := i + chunkSize
		if end > len(monitors) {
			end = len(monitors)
		}
		
		// Create sub-batch
		subBatch := MonitorBatch{
			Monitors: monitors[i:end],
			BatchID:  batch.BatchID,
			Offset:   batch.Offset + int64(i),
		}
		
		// Process the chunk
		if err := c.processBatch(subBatch); err != nil {
			return fmt.Errorf("failed to process chunk %d-%d of batch %d: %w", 
				i, end, batch.BatchID, err)
		}
		
		// Small yield to prevent blocking other operations
		if (i/chunkSize)%10 == 0 {
			time.Sleep(time.Microsecond)
		}
	}
	
	return nil
}


// updateStats updates creation statistics
func (c *StreamingEntityCreator) updateStats(entitiesInBatch int) {
	c.mu.Lock()
	c.entitiesCreated += int64(entitiesInBatch)
	c.batchesProcessed++
	c.mu.Unlock()
}

// reportProgress sends progress update
func (c *StreamingEntityCreator) reportProgress(progressChan chan<- EntityProgress) {
	if progressChan == nil {
		return
	}
	
	c.mu.RLock()
	entitiesCreated := c.entitiesCreated
	batchesProcessed := c.batchesProcessed
	c.mu.RUnlock()
	
	elapsed := time.Since(c.startTime)
	rate := float64(entitiesCreated) / elapsed.Seconds()
	
	// Get memory usage (rough estimate)
	memoryUsage := c.estimateMemoryUsage()
	
	select {
	case progressChan <- EntityProgress{
		EntitiesCreated:  entitiesCreated,
		BatchesProcessed: batchesProcessed,
		Rate:            rate,
		MemoryUsage:     memoryUsage,
	}:
	default:
		// Don't block if channel is full
	}
}

// estimateMemoryUsage provides a rough estimate of memory usage
func (c *StreamingEntityCreator) estimateMemoryUsage() int64 {
	// Rough estimation: each entity uses approximately 200 bytes
	// This includes components and ECS overhead
	c.mu.RLock()
	entitiesCreated := c.entitiesCreated
	c.mu.RUnlock()
	
	return entitiesCreated * 200
}

// GetStats returns current creation statistics
func (c *StreamingEntityCreator) GetStats() (entitiesCreated int64, batchesProcessed int64, rate float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	elapsed := time.Since(c.startTime)
	return c.entitiesCreated, c.batchesProcessed, float64(c.entitiesCreated) / elapsed.Seconds()
}